package control

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
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
func body(r *http.Request, value any) error {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return problem(415, "INVALID_REQUEST", "application/json required")
	}
	raw, e := io.ReadAll(io.LimitReader(r.Body, 1<<20+1))
	if e != nil || len(raw) > 1<<20 {
		return problem(413, "INVALID_REQUEST", "Request body too large")
	}
	if e = configfile.DecodeJSON(raw, value); e != nil {
		return problem(400, "INVALID_REQUEST", "Invalid or unknown JSON fields")
	}
	return nil
}

type rateEntry struct {
	Start time.Time
	Count int
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
	rateMu                 sync.Mutex
	rates                  map[string]rateEntry
	sendEmail              func(context.Context, string, string, string) error
}

func NewServer(ctx context.Context, c Config, store *Store) (*Server, error) {
	if e := c.Validate(); e != nil {
		return nil, e
	}
	s := &Server{Config: c, Store: store, providers: map[string]*provider{}, rates: map[string]rateEntry{}}
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
	if e := store.Update(ctx, func(st *State) error {
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
		if e := s.Store.View(r.Context(), func(*State) error { return nil }); e != nil {
			s.writeError(w, r, e)
			return
		}
		s.json(w, 200, map[string]string{"status": "ok"})
	})
	m.HandleFunc("GET /api/auth/providers", s.providersHandler)
	m.HandleFunc("GET /api/auth/csrf", s.csrfHandler)
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
		s.internalMux.HandleFunc("GET /api/internal/workers/artifacts/{artifact}", s.workerRuntimeArtifact)
		s.internalMux.HandleFunc("POST /api/internal/workers/ack", s.workerRuntimeAck)
	}
	s.internalMux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { s.writeError(w, r, notFound()) })
	if s.Config.StaticDir == "" {
		m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { s.writeError(w, r, notFound()) })
		return
	}
	fs := http.FileServer(http.Dir(s.Config.StaticDir))
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
func (s *Server) authenticate(st *State, r *http.Request) (Principal, error) {
	p := Principal{}
	cookie := s.cookie(r, "session")
	auth := r.Header.Get("Authorization")
	if len(r.Header.Values("Authorization")) > 1 {
		return p, problem(400, "AMBIGUOUS_CREDENTIAL", "Use one credential")
	}
	if cookie != "" && auth != "" {
		return p, problem(400, "AMBIGUOUS_CREDENTIAL", "Use one credential")
	}
	token := cookie
	if auth != "" {
		if !strings.HasPrefix(auth, "Bearer ") || strings.Count(auth, " ") != 1 {
			return p, problem(401, "UNAUTHENTICATED", "Authentication required")
		}
		token = strings.TrimPrefix(auth, "Bearer ")
	}
	if strings.HasPrefix(token, "krun_key_") {
		hash := digest(token)
		for id, h := range st.KeyHashes {
			if subtle.ConstantTimeCompare([]byte(hash), []byte(h)) != 1 {
				continue
			}
			k, ok := st.Keys[id]
			if !ok || !k.active() {
				break
			}
			p.User = st.Users[k.Owner]
			p.Kind = "api_key"
			p.Key = &k
			break
		}
	} else if token != "" {
		hash := digest(token)
		session, ok := st.Sessions[hash]
		if ok && session.CLI == (auth != "") && now().Before(session.Absolute) && now().Sub(session.Seen) < 30*time.Minute {
			u, exists := st.Users[session.User]
			if exists && u.AuthVersion == session.AuthVersion {
				p.User = u
				p.Kind = "session"
				p.SessionHash = hash
				session.Seen = now()
				st.Sessions[hash] = session
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
func (s *Server) protectedPrincipal(st *State, r *http.Request, sessionOnly bool) (Principal, error) {
	principal, err := s.authenticate(st, r)
	if err != nil {
		return Principal{}, err
	}
	if sessionOnly && principal.Kind == "api_key" {
		return Principal{}, forbidden()
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead && principal.Kind == "session" {
		if !s.validCSRF(r, st.Sessions[principal.SessionHash].CSRF) {
			return Principal{}, problem(403, "CSRF_INVALID", "CSRF verification failed")
		}
	}
	return principal, nil
}

func (s *Server) protected(sessionOnly bool, fn endpoint) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Read the bounded request outside the shared state transaction; slow clients must not hold the IAM lock.
		if r.Body != nil && r.Method != "GET" && r.Method != "HEAD" {
			raw, e := io.ReadAll(io.LimitReader(r.Body, (1<<20)+1))
			if e != nil || len(raw) > 1<<20 {
				s.writeError(w, r, problem(413, "INVALID_REQUEST", "Request body too large"))
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(raw))
		}
		var out any
		status := 200
		var appErr error
		err := s.Store.Update(r.Context(), func(st *State) error {
			s.cleanup(st)
			p, e := s.protectedPrincipal(st, r, sessionOnly)
			if e != nil {
				appErr = e
				return nil
			}
			raw, e := json.Marshal(st)
			if e != nil {
				return e
			}
			out, status, appErr = fn(st, p, r)
			if appErr != nil {
				restored := newState()
				if e = json.Unmarshal(raw, restored); e != nil {
					return e
				}
				*st = *restored
				s.audit(st, p, r, r.Method+" "+r.Pattern, r.PathValue("org"), false)
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
		if r.URL.Path == "/auth/logout" {
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
	s.Store.recordAudit(Audit{ID: randomID("audit"), Time: now(), Actor: p.User.ID, Credential: p.Kind, KeyID: key, Action: action, Resource: target, Organization: org, Allowed: allowed, RequestID: r.Header.Get("X-Krun-Request-ID"), Revision: st.Revision})
}
func (s *Server) require(st *State, p Principal, r *http.Request, action string, t Target) error {
	d, e := Authorize(st, p, action, t)
	if e != nil {
		return e
	}
	// Read decisions are not audited: polling traffic would grow the audit
	// log (and therefore the whole state blob) without bound. Denials stay.
	if d.Allowed && r.Method != http.MethodGet && r.Method != http.MethodHead {
		s.audit(st, p, r, action, t.ID, d.Allowed)
	}
	if !d.Allowed {
		s.audit(st, p, r, action, t.ID, d.Allowed)
		return forbidden()
	}
	return nil
}
func (s *Server) limited(r *http.Request) bool {
	s.rateMu.Lock()
	defer s.rateMu.Unlock()
	n := now()
	for k, e := range s.rates {
		if n.Sub(e.Start) > time.Minute {
			delete(s.rates, k)
		}
	}
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i >= 0 {
		host = host[:i]
	}
	e := s.rates[host]
	if n.Sub(e.Start) > time.Minute {
		e = rateEntry{Start: n}
	}
	e.Count++
	s.rates[host] = e
	return e.Count > 120 || len(s.rates) > 10000
}
func (s *Server) cleanup(st *State) {
	n := now()
	for k, v := range st.Sessions {
		if !n.Before(v.Absolute) || n.Sub(v.Seen) >= 30*time.Minute {
			delete(st.Sessions, k)
		}
	}
	for k, v := range st.Browsers {
		if !n.Before(v.Expires) {
			delete(st.Browsers, k)
		}
	}
	for k, v := range st.Logins {
		if !n.Before(v.Expires) {
			delete(st.Logins, k)
		}
	}
	for k, v := range st.EmailLogins {
		if !n.Before(v.Expires) {
			delete(st.EmailLogins, k)
		}
	}
	for k, v := range st.Devices {
		if !n.Before(v.Expires) {
			delete(st.Devices, k)
		}
	}
}
func (s *Server) me(st *State, p Principal, r *http.Request) (any, int, error) {
	return map[string]any{"user": p.User, "credential_kind": p.Kind, "system_admin": isAdmin(st, p.User, ""), "revision": st.Revision, "workers_configured": s.Config.Workers != nil}, 200, nil
}
func (s *Server) logout(st *State, p Principal, r *http.Request) (any, int, error) {
	delete(st.Sessions, p.SessionHash)
	if b, ok := st.Browsers[digest(s.cookie(r, "browser"))]; ok {
		b.Generation++
		st.Browsers[digest(s.cookie(r, "browser"))] = b
	}
	s.audit(st, p, r, "auth.logout", p.User.ID, true)
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
func (s *Server) csrfHandler(w http.ResponseWriter, r *http.Request) {
	token := ""
	browserToken := ""
	e := s.Store.Update(r.Context(), func(st *State) error {
		if session, ok := st.Sessions[digest(s.cookie(r, "session"))]; ok && now().Before(session.Absolute) && now().Sub(session.Seen) < 30*time.Minute {
			token = session.CSRF
			return nil
		}
		browserHash := digest(s.cookie(r, "browser"))
		b, ok := st.Browsers[browserHash]
		if !ok || !now().Before(b.Expires) {
			browserToken = randomToken()
			browserHash = digest(browserToken)
			b = Browser{CSRF: randomToken(), Expires: now().Add(15 * time.Minute)}
			st.Browsers[browserHash] = b
		}
		token = b.CSRF
		return nil
	})
	if e != nil {
		s.writeError(w, r, e)
		return
	}
	if browserToken != "" {
		s.setCookie(w, "browser", browserToken, 900)
	}
	s.json(w, 200, map[string]string{"csrf_token": token})
}
func Serve(ctx context.Context, c Config) error {
	store, e := OpenStore(ctx, c.DB)
	if e != nil {
		return e
	}
	defer store.Close()
	handler, e := NewServer(ctx, c, store)
	if e != nil {
		return e
	}
	if handler.workers != nil {
		go handler.workers.run(ctx)
	}
	retentionCtx, stopRetention := context.WithCancel(ctx)
	retentionDone := make(chan struct{})
	go func() {
		defer close(retentionDone)
		store.retainAudits(retentionCtx, time.Minute)
	}()
	defer func() { stopRetention(); <-retentionDone }()
	server := &http.Server{Addr: c.Listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 25 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	servers := []*http.Server{server}
	if c.InternalListen != "" {
		servers = append(servers, &http.Server{Addr: c.InternalListen, Handler: http.HandlerFunc(handler.serveInternal), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 35 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10})
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for _, srv := range servers {
			_ = srv.Shutdown(shutdown)
		}
	}()
	fmt.Print(localize.Format(localize.Language(ctx), "krun control server: %s/\n", c.PublicURL))
	for _, srv := range servers[1:] {
		go func(srv *http.Server) {
			e := srv.ListenAndServe()
			if !errors.Is(e, http.ErrServerClosed) && e != nil {
				fmt.Fprintln(os.Stderr, "internal listener:", e)
			}
		}(srv)
	}
	e = server.ListenAndServe()
	if errors.Is(e, http.ErrServerClosed) {
		return nil
	}
	return e
}
