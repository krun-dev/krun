package pgwire

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgproto3"
)

const (
	startupTimeout = 10 * time.Second
	idleTimeout    = 5 * time.Minute
	maxSessions    = 256
)

// Origin is a fixed PostgreSQL destination selected by the platform after
// authenticating the binding and checking the resolved address against its pool.
type Origin struct {
	ID       string
	Host     string
	Address  netip.AddrPort
	Database string
	User     string
	Password string
	RootCAs  *x509.CertPool
	Limit    int
}

type Authorize func(ctx context.Context, user, password string) (Origin, error)

type Server struct {
	tls       *tls.Config
	authorize Authorize
	mu        sync.Mutex
	origins   map[string]int
	cancel    map[[8]byte]func(context.Context) error
}

func New(tlsConfig *tls.Config, authorize Authorize) *Server {
	return &Server{
		tls:       tlsConfig,
		authorize: authorize,
		origins:   make(map[string]int),
		cancel:    make(map[[8]byte]func(context.Context) error),
	}
}

func (s *Server) Serve(ctx context.Context, listener net.Listener) error {
	ctx, cancel := context.WithCancel(ctx)
	closeListener := context.AfterFunc(ctx, func() { _ = listener.Close() })
	var sessions sync.WaitGroup
	defer func() {
		cancel()
		closeListener()
		sessions.Wait()
	}()

	slots := make(chan struct{}, maxSessions)
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		select {
		case slots <- struct{}{}:
		default:
			_ = conn.Close()
			continue
		}
		sessions.Go(func() {
			defer func() { <-slots }()
			s.session(ctx, conn)
		})
	}
}

func (s *Server) session(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	_ = conn.SetDeadline(time.Now().Add(startupTimeout))

	backend := pgproto3.NewBackend(conn, conn)
	startup, err := backend.ReceiveStartupMessage()
	if err != nil {
		return
	}
	if _, ok := startup.(*pgproto3.SSLRequest); !ok {
		return
	}
	if _, err := conn.Write([]byte{'S'}); err != nil {
		return
	}
	client := tls.Server(conn, s.tls)
	if err := client.HandshakeContext(ctx); err != nil {
		return
	}
	backend = pgproto3.NewBackend(client, client)
	backend.SetMaxBodyLen(4096)
	startup, err = backend.ReceiveStartupMessage()
	if err != nil {
		return
	}

	startCtx, cancel := context.WithTimeout(ctx, startupTimeout)
	defer cancel()
	switch message := startup.(type) {
	case *pgproto3.CancelRequest:
		s.cancelSession(startCtx, message)
	case *pgproto3.StartupMessage:
		s.forward(startCtx, client, backend, message)
	}
}

func (s *Server) forward(ctx context.Context, client net.Conn, backend *pgproto3.Backend, startup *pgproto3.StartupMessage) {
	backend.Send(&pgproto3.AuthenticationCleartextPassword{})
	if err := backend.SetAuthType(pgproto3.AuthTypeCleartextPassword); err != nil {
		return
	}
	if err := backend.Flush(); err != nil {
		return
	}
	message, err := backend.Receive()
	password, ok := message.(*pgproto3.PasswordMessage)
	if err != nil || !ok {
		return
	}
	origin, err := s.authorize(ctx, startup.Parameters["user"], password.Password)
	if err != nil || origin.ID == "" || !origin.Address.IsValid() || origin.Limit < 1 {
		sendError(backend, "28P01", "database binding authentication failed")
		return
	}

	if !s.acquire(origin) {
		sendError(backend, "53300", "database connection limit reached")
		return
	}
	defer s.release(origin.ID)

	upstream, err := dialOrigin(ctx, origin)
	if err != nil {
		sendError(backend, "08006", "origin database connection failed")
		return
	}
	if err := upstream.SyncConn(ctx); err != nil {
		_ = upstream.Close(ctx)
		sendError(backend, "08006", "origin database connection failed")
		return
	}
	hijacked, err := upstream.Hijack()
	if err != nil {
		_ = upstream.Close(ctx)
		sendError(backend, "08006", "origin database connection failed")
		return
	}
	defer hijacked.Conn.Close()

	key, err := s.registerCancel(upstream.CancelRequest)
	if err != nil {
		sendError(backend, "08006", "database session unavailable")
		return
	}
	defer func() {
		s.mu.Lock()
		delete(s.cancel, key)
		s.mu.Unlock()
	}()

	backend.Send(&pgproto3.AuthenticationOk{})
	for name, value := range hijacked.ParameterStatuses {
		backend.Send(&pgproto3.ParameterStatus{Name: name, Value: value})
	}
	backend.Send(&pgproto3.BackendKeyData{ProcessID: binary.BigEndian.Uint32(key[:4]), SecretKey: key[4:]})
	backend.Send(&pgproto3.ReadyForQuery{TxStatus: hijacked.TxStatus})
	if err := backend.Flush(); err != nil {
		return
	}
	_ = client.SetDeadline(time.Time{})
	relay(client, hijacked.Conn)
}

func Probe(ctx context.Context, origin Origin) error {
	conn, err := dialOrigin(ctx, origin)
	if err != nil {
		return err
	}
	return conn.Close(ctx)
}

func dialOrigin(ctx context.Context, origin Origin) (*pgconn.PgConn, error) {
	dsn := url.URL{
		Scheme: "postgresql",
		User:   url.UserPassword(origin.User, origin.Password),
		Host:   net.JoinHostPort(origin.Host, strconv.Itoa(int(origin.Address.Port()))),
		Path:   "/" + origin.Database,
	}
	dsn.RawQuery = "sslmode=disable&sslnegotiation=postgres&gssencmode=disable"
	cfg, err := pgconn.ParseConfig(dsn.String())
	if err != nil {
		return nil, err
	}
	cfg.RuntimeParams = nil
	cfg.Fallbacks = nil
	cfg.TLSConfig = &tls.Config{
		MinVersion: tls.VersionTLS12,
		ServerName: origin.Host,
		RootCAs:    origin.RootCAs,
	}
	cfg.LookupFunc = func(context.Context, string) ([]string, error) {
		return []string{origin.Address.Addr().String()}, nil
	}
	cfg.DialFunc = func(ctx context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" || address != origin.Address.String() {
			return nil, errors.New("database destination changed")
		}
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	return pgconn.ConnectConfig(ctx, cfg)
}

func (s *Server) acquire(origin Origin) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.origins[origin.ID] >= origin.Limit {
		return false
	}
	s.origins[origin.ID]++
	return true
}

func (s *Server) release(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.origins[id] == 1 {
		delete(s.origins, id)
		return
	}
	s.origins[id]--
}

func (s *Server) registerCancel(cancel func(context.Context) error) ([8]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for {
		var key [8]byte
		if _, err := rand.Read(key[:]); err != nil {
			return key, err
		}
		if _, exists := s.cancel[key]; !exists {
			s.cancel[key] = cancel
			return key, nil
		}
	}
}

func (s *Server) cancelSession(ctx context.Context, message *pgproto3.CancelRequest) {
	if len(message.SecretKey) != 4 {
		return
	}
	var key [8]byte
	binary.BigEndian.PutUint32(key[:4], message.ProcessID)
	copy(key[4:], message.SecretKey)
	s.mu.Lock()
	cancel := s.cancel[key]
	s.mu.Unlock()
	if cancel != nil {
		_ = cancel(ctx)
	}
}

func sendError(backend *pgproto3.Backend, code, message string) {
	backend.Send(&pgproto3.ErrorResponse{Severity: "FATAL", Code: code, Message: message})
	_ = backend.Flush()
}

type idleConn struct{ net.Conn }

func (conn idleConn) Read(p []byte) (int, error) {
	_ = conn.SetReadDeadline(time.Now().Add(idleTimeout))
	return conn.Conn.Read(p)
}

func (conn idleConn) Write(p []byte) (int, error) {
	_ = conn.SetWriteDeadline(time.Now().Add(idleTimeout))
	return conn.Conn.Write(p)
}

func relay(client, upstream net.Conn) {
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(idleConn{upstream}, idleConn{client})
		_ = upstream.Close()
		close(done)
	}()
	_, _ = io.Copy(idleConn{client}, idleConn{upstream})
	_ = client.Close()
	<-done
}
