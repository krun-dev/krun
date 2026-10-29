package control

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/krun-dev/krun/internal/configfile"
	"github.com/krun-dev/krun/internal/localize"
)

type APIError struct {
	Status        int
	Code, Message string
}

func (e *APIError) Error() string                    { return e.Code }
func problem(status int, code, message string) error { return &APIError{status, code, message} }
func forbidden() error                               { return problem(403, "PERMISSION_DENIED", "Permission denied") }
func notFound() error                                { return problem(404, "RESOURCE_NOT_FOUND", "Resource not found") }

const apiJSONBodyMax = 1 << 20

func body(r *http.Request, value any) error {
	return bodyWithLimit(r, value, apiJSONBodyMax)
}

func bodyWithLimit(r *http.Request, value any, limit int64) error {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return problem(415, "INVALID_REQUEST", "application/json required")
	}
	raw, e := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if e != nil || int64(len(raw)) > limit {
		return problem(413, "INVALID_REQUEST", "Request body too large")
	}
	if e = configfile.DecodeJSON(raw, value); e != nil {
		return problem(400, "INVALID_REQUEST", "Invalid or unknown JSON fields")
	}
	return nil
}

type Server struct {
	workers                *workersController
	workerInvokeClient     *http.Client
	workerRepositoryClient *http.Client
	Config                 Config
	Store                  *Store
	mux                    *http.ServeMux
	internalMux            *http.ServeMux
	providers              map[string]*provider
	trustedProxies         []*net.IPNet
	sendEmail              func(context.Context, string, string, string) error
}

func NewServer(ctx context.Context, c Config, store *Store) (*Server, error) {
	if e := c.Validate(); e != nil {
		return nil, e
	}
	s := &Server{Config: c, Store: store, providers: map[string]*provider{}}
	for _, cidr := range c.TrustedProxyCIDRs {
		_, network, _ := net.ParseCIDR(cidr) // Config.Validate already checked it.
		s.trustedProxies = append(s.trustedProxies, network)
	}
	s.workerInvokeClient = newWorkerInvokeClient()
	s.workerRepositoryClient = newWorkerRepositoryClient()
	s.sendEmail = c.sendVerificationEmail
	client := upstreamClient(c)
	for _, pc := range c.Providers {
		if !pc.enabled() {
			continue
		}
		p, e := newProvider(ctx, c, pc, client)
		if e != nil {
			return nil, e
		}
		s.providers[pc.ID] = p
	}
	if e := store.Update(ctx, PartitionIAM, func(st *State) error {
		for _, p := range s.providers {
			fp := fingerprint(p.config)
			if old, ok := st.Sources[p.config.Source]; ok && old != fp {
				return errors.New("existing identity source namespace cannot be replaced")
			}
			providerKey := "provider:" + p.config.ID
			namespaceKey := "namespace:" + namespace(p.config)
			if old, ok := st.Sources[providerKey]; ok && old != p.config.Source {
				return errors.New("provider identity source cannot be reassigned")
			}
			if old, ok := st.Sources[namespaceKey]; ok && old != p.config.Source {
				return errors.New("identity namespace already registered")
			}
			st.Sources[providerKey] = p.config.Source
			st.Sources[namespaceKey] = p.config.Source
			st.Sources[p.config.Source] = fp
		}
		return nil
	}); e != nil {
		return nil, e
	}
	if c.Workers != nil {
		var err error
		s.workers, err = newWorkersController(s)
		if err != nil {
			return nil, err
		}
	}
	s.routes()
	return s, nil
}
func (s *Server) routes() {
	m := http.NewServeMux()
	s.mux = m
	m.HandleFunc("GET /api/healthz", func(w http.ResponseWriter, r *http.Request) {
		if e := s.Store.Ping(r.Context()); e != nil {
			s.writeError(w, r, e)
			return
		}
		s.json(w, 200, map[string]string{"status": "ok"})
	})
	m.HandleFunc("GET /api/auth/providers", s.providersHandler)
	m.HandleFunc("POST /api/auth/csrf", s.csrfHandler)
	m.HandleFunc("POST /api/auth/{provider}/login", s.loginHandler)
	m.HandleFunc("POST /api/auth/{provider}/email/code", s.emailCodeHandler)
	m.HandleFunc("POST /api/auth/{provider}/email/verify", s.emailVerifyHandler)
	m.HandleFunc("GET /api/auth/{provider}/callback", s.callbackHandler)
	m.HandleFunc("GET /api/auth/me", s.protected(false, s.me))
	m.HandleFunc("POST /api/auth/logout", s.protected(true, s.logout))
	m.HandleFunc("POST /api/auth/cli/start", s.deviceStart)
	m.HandleFunc("POST /api/auth/cli/poll", s.devicePoll)
	m.HandleFunc("POST /api/auth/cli/approve", s.protected(true, s.deviceApprove))
	s.apiRoutes(m)
	s.workersRoutes(m)
	m.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { s.writeError(w, r, notFound()) })
	s.internalMux = http.NewServeMux()
	if s.Config.Workers != nil {
		s.internalMux.HandleFunc("GET /api/internal/workers/config/snapshot", s.workerRuntimeConfigSnapshot)
		s.internalMux.HandleFunc("GET /api/internal/workers/config/changes", s.workerRuntimeConfigChanges)
		s.internalMux.HandleFunc("GET /api/internal/workers/releases/{release}/assets", s.workerRuntimeReleaseAssets)
		s.internalMux.HandleFunc("GET /api/internal/workers/releases/{release}/assets/{path...}", s.workerRuntimeReleaseAssetURL)
		s.internalMux.HandleFunc("GET /api/internal/workers/releases/{release}/secrets", s.workerRuntimeReleaseSecrets)
		s.internalMux.HandleFunc("GET /api/internal/workers/artifacts/{artifact}", s.workerRuntimeArtifact)
		s.internalMux.HandleFunc("POST /api/internal/workers/ack", s.workerRuntimeAck)
	}
	s.internalMux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { s.writeError(w, r, notFound()) })
	if s.Config.StaticDir == "" {
		m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { s.writeError(w, r, notFound()) })
		return
	}
	staticRoot := s.Config.StaticDir
	// The deployable frontend is deliberately isolated under dist/console so
	// uploading the console cannot accidentally include server binaries or
	// other repository output. For local all-in-one serving, accept the build
	// root (dist) and descend into that static artifact automatically.
	if _, err := os.Stat(filepath.Join(staticRoot, "index.html")); errors.Is(err, os.ErrNotExist) {
		consoleRoot := filepath.Join(staticRoot, "console")
		if _, consoleErr := os.Stat(filepath.Join(consoleRoot, "index.html")); consoleErr == nil {
			staticRoot = consoleRoot
		}
	}
	fs := http.FileServer(http.Dir(staticRoot))
	publicOrigin, _ := url.Parse(s.Config.PublicURL)
	m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" {
			s.writeError(w, r, notFound())
			return
		}
		if s.Config.Development && r.Host != publicOrigin.Host {
			target := s.Config.PublicURL + r.URL.EscapedPath()
			if r.URL.RawQuery != "" {
				target += "?" + r.URL.RawQuery
			}
			w.Header().Set("Cache-Control", "no-store")
			http.Redirect(w, r, target, http.StatusSeeOther)
			return
		}
		fs.ServeHTTP(w, r)
	})
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Language", localize.RequestLanguage(r.Header.Get("Accept-Language")))
	w.Header().Add("Vary", "Accept-Language")
	r.Header.Set("X-Krun-Request-ID", randomID("req"))
	w.Header().Set("X-Request-ID", r.Header.Get("X-Krun-Request-ID"))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
	if strings.HasPrefix(r.URL.Path, "/api/") {
		w.Header().Set("Cache-Control", "no-store")
	}
	s.mux.ServeHTTP(w, r)
}

// serveInternal fronts the runtime-only listener: /api/internal/* only, never static files.
func (s *Server) serveInternal(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	s.internalMux.ServeHTTP(w, r)
}
func (s *Server) json(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if status != 204 {
		_ = json.NewEncoder(w).Encode(v)
	}
}
func (s *Server) writeError(w http.ResponseWriter, r *http.Request, e error) {
	a := &APIError{503, "AUTHORIZATION_UNAVAILABLE", "Control service unavailable"}
	var target *APIError
	if errors.As(e, &target) {
		a = target
	} else {
		// Keep internal details out of the response, but preserve enough server-side
		// context to correlate a generic 503 with the actual storage/runtime error.
		slog.Error("request failed", "error", e, "request_id", r.Header.Get("X-Krun-Request-ID"), "method", r.Method, "path", r.URL.Path)
	}
	s.json(w, a.Status, map[string]any{"error": map[string]string{"code": a.Code, "message": localize.Text(localize.RequestLanguage(r.Header.Get("Accept-Language")), a.Message)}, "request_id": r.Header.Get("X-Krun-Request-ID")})
}
func (s *Server) cookieName(kind string) string {
	if s.Config.Development {
		return "krun_dev_" + kind
	}
	return "__Host-krun_" + kind
}
func (s *Server) setCookie(w http.ResponseWriter, kind, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: s.cookieName(kind), Value: value, Path: "/", HttpOnly: true, Secure: !s.Config.Development, SameSite: http.SameSiteLaxMode, MaxAge: maxAge})
}
func (s *Server) cookie(r *http.Request, kind string) string {
	c, e := r.Cookie(s.cookieName(kind))
	if e != nil {
		return ""
	}
	return c.Value
}
func (s *Server) validCSRF(r *http.Request, expected string) bool {
	if r.Header.Get("Origin") != s.Config.PublicURL {
		return false
	}
	value := r.Header.Get("X-CSRF-Token")
	if value == "" && strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		value = r.FormValue("csrf_token")
	}
	return value != "" && expected != "" && subtle.ConstantTimeCompare([]byte(value), []byte(expected)) == 1
}
func (s *Server) sourceEnabled(source string) bool {
	for _, p := range s.providers {
		if p.config.Source == source {
			return true
		}
	}
	return false
}

// credentials are the single-row lookups resolved before the IAM snapshot is
// loaded. Keeping them separate is what makes an authenticated read cost one
// session lookup instead of a whole-document write.
type authCredentials struct {
	session     Session
	sessionHash string
	hasSession  bool
	isKey       bool
	keyID       string
}

func (s *Server) readCredentials(r *http.Request) (authCredentials, error) {
	c := authCredentials{}
	cookie := s.cookie(r, "session")
	auth := r.Header.Get("Authorization")
	if len(r.Header.Values("Authorization")) > 1 {
		return c, problem(400, "AMBIGUOUS_CREDENTIAL", "Use one credential")
	}
	if cookie != "" && auth != "" {
		return c, problem(400, "AMBIGUOUS_CREDENTIAL", "Use one credential")
	}
	token := cookie
	if auth != "" {
		if !strings.HasPrefix(auth, "Bearer ") || strings.Count(auth, " ") != 1 {
			return c, problem(401, "UNAUTHENTICATED", "Authentication required")
		}
		token = strings.TrimPrefix(auth, "Bearer ")
	}
	switch {
	case strings.HasPrefix(token, "krun_key_"):
		c.isKey = true
		keyID, ok, err := s.Store.apiKeyIDByHash(r.Context(), digest(token))
		if err != nil {
			return c, err
		}
		if ok {
			c.keyID = keyID
		}
	case token != "":
		session, ok, err := s.Store.sessionByHash(r.Context(), digest(token))
		if err != nil {
			return c, err
		}
		c.session, c.hasSession = session, ok
		c.sessionHash = digest(token)
	}
	return c, nil
}

// authenticate resolves credentials against the loaded IAM state. It performs
// no writes: refreshing last-seen is a separate throttled statement issued by
// protected() only after the request succeeds.
func (s *Server) authenticate(st *State, c authCredentials, r *http.Request) (Principal, error) {
	p := Principal{}
	if c.isKey {
		if k, ok := st.Keys[c.keyID]; ok && k.active() {
			p.User = st.Users[k.Owner]
			p.Kind = "api_key"
			p.Key = &k
		}
	} else if c.hasSession {
		session := c.session
		// A bearer token must never be satisfied by a cookie session, or the
		// reverse: CLI sessions are only reachable with an Authorization header.
		if session.CLI == (r.Header.Get("Authorization") != "") && now().Before(session.Absolute) && now().Sub(session.Seen) < 30*time.Minute {
			u, exists := st.Users[session.User]
			if exists && u.AuthVersion == session.AuthVersion {
				p.User = u
				p.Kind = "session"
				p.SessionHash = session.Hash
				if session.CLI {
					p.Kind = "cli_session"
				}
			}
		}
	}
	if p.User.ID == "" || p.User.Status != "active" || !s.sourceEnabled(p.User.Source) {
		return Principal{}, problem(401, "UNAUTHENTICATED", "Authentication required")
	}
	return p, nil
}

type endpoint func(*State, Principal, *http.Request) (any, int, error)

// Shared credential and CSRF checks for handlers that use a state transaction.
func (s *Server) protectedPrincipal(st *State, c authCredentials, r *http.Request, sessionOnly bool) (Principal, error) {
	principal, err := s.authenticate(st, c, r)
	if err != nil {
		return Principal{}, err
	}
	if sessionOnly && principal.Kind == "api_key" {
		return Principal{}, forbidden()
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead && principal.Kind == "session" {
		if !c.hasSession || !s.validCSRF(r, c.session.CSRF) {
			return Principal{}, problem(403, "CSRF_INVALID", "CSRF verification failed")
		}
	}
	return principal, nil
}

func (s *Server) protected(sessionOnly bool, fn endpoint) http.HandlerFunc {
	return s.protectedWith(sessionOnly, PartitionIAM, PartitionIAM, apiJSONBodyMax, fn)
}

// protectedWorkers serves the Workers API. Authorization reads IAM, the handler
// reads and writes Workers rows, and only Workers rows are persisted — IAM and
// Workers never share a transaction.
func (s *Server) protectedWorkers(sessionOnly bool, fn endpoint) http.HandlerFunc {
	return s.protectedWorkersWithBodyLimit(sessionOnly, apiJSONBodyMax, fn)
}

func (s *Server) protectedWorkersWithBodyLimit(sessionOnly bool, bodyLimit int64, fn endpoint) http.HandlerFunc {
	return s.protectedWith(sessionOnly, PartitionAll, PartitionWorkers, bodyLimit, fn)
}

func (s *Server) protectedWith(sessionOnly bool, load, persist StatePartition, bodyLimit int64, fn endpoint) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		requestPersist := persist
		c, e := s.readCredentials(r)
		if e != nil {
			s.writeError(w, r, e)
			return
		}
		// Credential lookups above are single-row indexed reads. Reject a
		// missing or unknown credential before reading a potentially slow body
		// or loading an IAM/Workers snapshot.
		if !c.hasSession && c.keyID == "" {
			s.writeError(w, r, problem(401, "UNAUTHENTICATED", "Authentication required"))
			return
		}
		// Read the bounded request outside the state transaction; slow clients
		// must not hold a database transaction open.
		if r.Body != nil && r.Method != "GET" && r.Method != "HEAD" {
			raw, e := io.ReadAll(io.LimitReader(r.Body, bodyLimit+1))
			if e != nil || int64(len(raw)) > bodyLimit {
				s.writeError(w, r, problem(413, "INVALID_REQUEST", "Request body too large"))
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(raw))
		}
		// A read must never write, including on authorization denial.
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			requestPersist = 0
		}
		var out any
		status := 200
		var appErr error
		err := s.Store.Mutate(r.Context(), load, requestPersist, func(st *State) error {
			p, e := s.protectedPrincipal(st, c, r, sessionOnly)
			if e != nil {
				appErr = e
				return nil
			}
			// Snapshot the loaded state so a rejected handler cannot leave a
			// partial mutation behind while its denial audit still commits.
			var checkpoint *State
			if requestPersist != 0 {
				checkpoint, e = cloneState(st)
				if e != nil {
					return e
				}
			}
			out, status, appErr = fn(st, p, r)
			if appErr != nil {
				if checkpoint != nil {
					*st = *checkpoint
				}
				if r.Method != http.MethodGet && r.Method != http.MethodHead {
					s.audit(st, p, r, r.Method+" "+r.Pattern, r.PathValue("org"), false)
				}
			}
			return nil
		})
		if err != nil {
			appErr = err
		}
		if appErr != nil {
			s.writeError(w, r, appErr)
			return
		}
		// Last-seen is best-effort and throttled; it must never turn a read into
		// a failure, and it is skipped for API-key requests.
		if c.hasSession {
			if e := s.Store.touchSession(r.Context(), c.sessionHash); e != nil && r.Context().Err() == nil {
				slog.Warn("session touch failed", "error", e)
			}
		}
		if r.URL.Path == "/api/auth/logout" {
			s.setCookie(w, "session", "", -1)
		}
		s.json(w, status, out)
	}
}
func (s *Server) audit(st *State, p Principal, r *http.Request, action, target string, allowed bool) {
	key := ""
	if p.Key != nil {
		key = p.Key.ID
	}
	org := ""
	if _, ok := st.Organizations[target]; ok {
		org = target
	}
	if res, ok := st.Resources[target]; ok {
		org = res.Organization
	}
	if key, ok := st.Keys[target]; ok {
		org = key.Organization
	}
	st.recordAudit(Audit{ID: randomID("audit"), Time: now(), Actor: p.User.ID, Credential: p.Kind, KeyID: key, Action: action, Resource: target, Organization: org, Allowed: allowed, RequestID: r.Header.Get("X-Krun-Request-ID"), Revision: st.Revision})
}
func (s *Server) require(st *State, p Principal, r *http.Request, action string, t Target) error {
	d, e := Authorize(st, p, action, t)
	if e != nil {
		return e
	}
	// Reads, including denials, never append audit rows. A caller can otherwise
	// turn an inexpensive forbidden GET into an unbounded database write path.
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		s.audit(st, p, r, action, t.ID, d.Allowed)
	}
	if !d.Allowed {
		return forbidden()
	}
	return nil
}
func (s *Server) checkRateLimit(r *http.Request) error {
	exceeded, err := s.Store.RateLimitExceeded(r.Context(), "auth", s.clientIP(r), time.Minute, 120)
	if err != nil {
		return err
	}
	if exceeded {
		return problem(429, "RATE_LIMITED", "Try again later")
	}
	return nil
}

func (s *Server) trustedProxy(ip net.IP) bool {
	for _, network := range s.trustedProxies {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

// clientIP walks a trusted X-Forwarded-For chain from the nearest hop toward
// the browser. A malformed chain is ignored in full; accepting only its useful
// prefix would let an attacker influence the result.
func (s *Server) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer := net.ParseIP(host)
	if peer == nil || !s.trustedProxy(peer) {
		return host
	}
	raw := strings.Join(r.Header.Values("X-Forwarded-For"), ",")
	parts := strings.Split(raw, ",")
	chain := make([]net.IP, 0, len(parts))
	for _, part := range parts {
		ip := net.ParseIP(strings.TrimSpace(part))
		if ip == nil {
			return host
		}
		chain = append(chain, ip)
	}
	for i := len(chain) - 1; i >= 0; i-- {
		if !s.trustedProxy(chain[i]) || i == 0 {
			return chain[i].String()
		}
	}
	return host
}

// cleanup is gone from the request path: expired sessions, browsers and login
// transactions are deleted by the background retention loop in retention.go.
func (s *Server) me(st *State, p Principal, r *http.Request) (any, int, error) {
	return map[string]any{"user": p.User, "credential_kind": p.Kind, "system_admin": isAdmin(st, p.User, ""), "revision": st.Revision, "workers_configured": s.Config.Workers != nil}, 200, nil
}

// logout deletes this session row and bumps the browser generation so a
// concurrent login cannot be replayed onto the just-closed browser.
func (s *Server) logout(st *State, p Principal, r *http.Request) (any, int, error) {
	browserHash := digest(s.cookie(r, "browser"))
	s.audit(st, p, r, "auth.logout", p.User.ID, true)
	if s.Store.pool != nil {
		audits := append([]Audit(nil), st.pendingAudits...)
		st.pendingAudits = nil
		if e := s.Store.logoutSessionAudited(r.Context(), p.SessionHash, browserHash, audits); e != nil {
			return nil, 0, e
		}
		return nil, 204, nil
	}
	if e := s.Store.deleteSession(r.Context(), p.SessionHash); e != nil {
		return nil, 0, e
	}
	if browser, ok, e := s.Store.browserFor(r.Context(), browserHash); e != nil {
		return nil, 0, e
	} else if ok {
		browser.Generation++
		if e = s.Store.upsertBrowser(r.Context(), browserHash, browser); e != nil {
			return nil, 0, e
		}
	}
	return nil, 204, nil
}
func (s *Server) providersHandler(w http.ResponseWriter, r *http.Request) {
	out := []map[string]string{}
	for _, pc := range s.Config.Providers {
		if !pc.enabled() {
			continue
		}
		out = append(out, map[string]string{"id": pc.ID, "type": pc.Type, "name": pc.Name})
	}
	s.json(w, 200, map[string]any{"providers": out, "development": s.Config.Development, "organization_self_service": s.Config.OrganizationSelfService})
}

// csrfHandler issues the token a browser needs before a mutating request. It
// touches only the session or browser row involved.
func (s *Server) csrfHandler(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Origin") != s.Config.PublicURL {
		s.writeError(w, r, problem(403, "CSRF_INVALID", "CSRF verification failed"))
		return
	}
	sessionHash := digest(s.cookie(r, "session"))
	if session, ok, e := s.Store.sessionByHash(r.Context(), sessionHash); e != nil {
		s.writeError(w, r, e)
		return
	} else if ok && now().Before(session.Absolute) && now().Sub(session.Seen) < 30*time.Minute {
		s.json(w, 200, map[string]string{"csrf_token": session.CSRF})
		return
	}
	browserToken := ""
	browserHash := digest(s.cookie(r, "browser"))
	browser, ok, e := s.Store.browserFor(r.Context(), browserHash)
	if e != nil {
		s.writeError(w, r, e)
		return
	}
	if !ok || !now().Before(browser.Expires) {
		exceeded, err := s.Store.RateLimitExceeded(r.Context(), "csrf", s.clientIP(r), time.Minute, 30)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		if exceeded {
			s.writeError(w, r, problem(429, "RATE_LIMITED", "Try again later"))
			return
		}
		browserToken = randomToken()
		browserHash = digest(browserToken)
		browser = Browser{CSRF: randomToken(), Expires: now().Add(15 * time.Minute)}
		if e = s.Store.upsertBrowser(r.Context(), browserHash, browser); e != nil {
			s.writeError(w, r, e)
			return
		}
	}
	if browserToken != "" {
		s.setCookie(w, "browser", browserToken, 900)
	}
	s.json(w, 200, map[string]string{"csrf_token": browser.CSRF})
}
func Serve(ctx context.Context, c Config) error {
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	store, e := OpenStore(ctx, c.DB)
	if e != nil {
		return e
	}
	defer store.Close()
	handler, e := NewServer(ctx, c, store)
	if e != nil {
		return e
	}
	workersDone := make(chan struct{})
	if handler.workers != nil {
		go func() {
			defer close(workersDone)
			handler.workers.run(runCtx)
		}()
	} else {
		close(workersDone)
	}
	retentionCtx, stopRetention := context.WithCancel(runCtx)
	retentionDone := make(chan struct{})
	go func() {
		defer close(retentionDone)
		// Session / login expiry and every per-build, per-release row is pruned
		// here instead of on the request path.
		store.retain(retentionCtx, time.Minute)
	}()
	server := &http.Server{Addr: c.Listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 25 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	servers := []*http.Server{server}
	if c.InternalListen != "" {
		servers = append(servers, &http.Server{Addr: c.InternalListen, Handler: http.HandlerFunc(handler.serveInternal), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 35 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10})
	}
	fmt.Print(localize.Format(localize.Language(ctx), "krun control server: %s/\n", c.PublicURL))
	err := serveHTTPServers(runCtx, servers)
	// Stop every background user of the Store before its deferred Close runs.
	// A listener bind failure follows the same order as a normal shutdown.
	cancelRun()
	stopRetention()
	<-retentionDone
	<-workersDone
	return err
}

// serveHTTPServers treats every configured listener as required. In
// particular, a failed internal runtime listener must fail the process instead
// of leaving a superficially healthy public server with a broken data plane.
func serveHTTPServers(ctx context.Context, servers []*http.Server) error {
	errCh := make(chan error, len(servers))
	var wg sync.WaitGroup
	for _, srv := range servers {
		wg.Add(1)
		go func(srv *http.Server) {
			defer wg.Done()
			err := srv.ListenAndServe()
			if errors.Is(err, http.ErrServerClosed) {
				err = nil
			}
			if err != nil {
				err = fmt.Errorf("listen %s: %w", srv.Addr, err)
			}
			errCh <- err
		}(srv)
	}
	var result error
	select {
	case <-ctx.Done():
	case result = <-errCh:
		if result == nil && ctx.Err() == nil {
			result = errors.New("HTTP listener stopped unexpectedly")
		}
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	for _, srv := range servers {
		_ = srv.Shutdown(shutdown)
	}
	cancel()
	wg.Wait()
	return result
}
