package control

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

// User-level OAuth connections for repository providers. A user connects
// their own GitHub/GitLab account once; builds they start fetch source with
// their grant, so repository visibility equals the user's own access.

func (s *Server) repositoryOAuthConfig(p WorkerRepositoryProvider) *oauth2.Config {
	if p.ClientID == "" {
		return nil
	}
	endpoint := oauth2.Endpoint{AuthURL: p.BaseURL + "/oauth/authorize", TokenURL: p.BaseURL + "/oauth/token"}
	if p.Type == "github" {
		endpoint = oauth2.Endpoint{AuthURL: "https://github.com/login/oauth/authorize", TokenURL: "https://github.com/login/oauth/access_token"}
	}
	scopes := []string{"read_repository"}
	if p.Type == "github" {
		scopes = []string{"repo"}
	}
	return &oauth2.Config{ClientID: p.ClientID, ClientSecret: p.ClientSecret, Endpoint: endpoint, Scopes: scopes, RedirectURL: s.Config.PublicURL + "/api/v1/workers/repository/" + p.ID + "/callback"}
}

// repositoryOAuthContext confines client credentials to the configured token
// endpoint. Redirects are rejected before net/http can replay a POST carrying
// client_secret, and the transport cannot dial a different host or port.
func (s *Server) repositoryOAuthContext(parent context.Context, provider WorkerRepositoryProvider) (context.Context, context.CancelFunc, error) {
	confBase := strings.TrimSuffix(provider.BaseURL, "/")
	tokenURL := confBase + "/oauth/token"
	if provider.Type == "github" {
		tokenURL = "https://github.com/login/oauth/access_token"
	}
	endpoint, err := url.Parse(tokenURL)
	if err != nil || endpoint.Hostname() == "" {
		return nil, nil, errors.New("invalid repository OAuth token endpoint")
	}
	expectedHost := strings.ToLower(endpoint.Hostname())
	expectedIP := net.ParseIP(expectedHost)
	loopbackHost := expectedHost == "localhost" || expectedIP != nil && expectedIP.IsLoopback()
	if endpoint.Scheme != "https" && !(s.Config.Development && endpoint.Scheme == "http" && loopbackHost) {
		return nil, nil, errors.New("repository OAuth token endpoint requires HTTPS")
	}
	expectedPort := endpoint.Port()
	if expectedPort == "" {
		if endpoint.Scheme == "https" {
			expectedPort = "443"
		} else {
			expectedPort = "80"
		}
	}
	transport := &http.Transport{
		Proxy:                  nil,
		TLSHandshakeTimeout:    5 * time.Second,
		ResponseHeaderTimeout:  5 * time.Second,
		MaxResponseHeaderBytes: 32 << 10,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, splitErr := net.SplitHostPort(address)
			if splitErr != nil || strings.ToLower(host) != expectedHost || port != expectedPort {
				return nil, errors.New("repository OAuth upstream host not allowed")
			}
			ips, resolveErr := net.DefaultResolver.LookupIPAddr(ctx, host)
			if resolveErr != nil {
				return nil, resolveErr
			}
			if len(ips) == 0 {
				return nil, errors.New("repository OAuth upstream has no addresses")
			}
			for _, candidate := range ips {
				private := candidate.IP.IsLoopback() || candidate.IP.IsPrivate()
				alwaysUnsafe := candidate.IP.IsUnspecified() || candidate.IP.IsLinkLocalUnicast() || candidate.IP.IsLinkLocalMulticast()
				loopbackTest := s.Config.Development && candidate.IP.IsLoopback() && loopbackHost
				if alwaysUnsafe || private && !loopbackTest && !provider.AllowPrivateNetwork {
					return nil, errors.New("unsafe repository OAuth upstream address")
				}
			}
			dialer := net.Dialer{Timeout: 5 * time.Second}
			return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
		},
	}
	client := &http.Client{
		Transport: repositoryOAuthResponseLimit{base: transport},
		Timeout:   15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errUpstreamRedirect
		},
	}
	ctx, stop := context.WithTimeout(parent, 15*time.Second)
	cancel := func() {
		stop()
		transport.CloseIdleConnections()
	}
	return context.WithValue(ctx, oauth2.HTTPClient, client), cancel, nil
}

const repositoryOAuthResponseLimitBytes = 64 << 10

type repositoryOAuthResponseLimit struct {
	base http.RoundTripper
}

func (t repositoryOAuthResponseLimit) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(request)
	if err == nil && response.Body != nil {
		response.Body = &repositoryOAuthBody{body: response.Body, remaining: repositoryOAuthResponseLimitBytes}
	}
	return response, err
}

type repositoryOAuthBody struct {
	body      io.ReadCloser
	remaining int64
}

func (b *repositoryOAuthBody) Read(buffer []byte) (int, error) {
	if b.remaining == 0 {
		var probe [1]byte
		n, err := b.body.Read(probe[:])
		if n > 0 {
			return 0, errors.New("repository OAuth response exceeds limit")
		}
		return 0, err
	}
	if int64(len(buffer)) > b.remaining {
		buffer = buffer[:b.remaining]
	}
	n, err := b.body.Read(buffer)
	b.remaining -= int64(n)
	return n, err
}

func (b *repositoryOAuthBody) Close() error { return b.body.Close() }

// workerRepositoryConnect starts a connection: returns the provider's
// authorization URL with CSRF state bound to this user and browser.
func (s *Server) workerRepositoryConnect(st *State, p Principal, r *http.Request) (any, int, error) {
	target, err := s.orgTarget(st, r)
	if err != nil {
		return nil, 0, err
	}
	if err = s.require(st, p, r, "service.list", target); err != nil {
		return nil, 0, err
	}
	provider, err := s.repositoryProvider(r.PathValue("provider"), r.PathValue("org"))
	if err != nil {
		return nil, 0, err
	}
	conf := s.repositoryOAuthConfig(provider)
	if conf == nil {
		return nil, 0, problem(409, "WORKER_REPOSITORY_OAUTH_DISABLED", "This provider is not configured for user connections")
	}
	state := randomToken()
	pending := WorkerRepositoryConnectState{User: p.User.ID, Provider: provider.ID, Organization: target.ID, Browser: digest(s.cookie(r, "browser")), Expires: now().Add(10 * time.Minute)}
	st.recordAudit(s.repositoryAudit(p, r, "workers.repository.connect.start", provider.ID, target.ID, true))
	if s.Store.pool != nil {
		audits := append([]Audit(nil), st.pendingAudits...)
		st.pendingAudits = nil
		err = s.Store.upsertRepositoryConnectStateAudited(r.Context(), state, pending, audits)
	} else {
		err = s.Store.upsertRepositoryConnectState(r.Context(), state, pending)
	}
	if err != nil {
		return nil, 0, err
	}
	return map[string]string{"authorization_url": conf.AuthCodeURL(state)}, 200, nil
}

// workerRepositoryCallback exchanges the OAuth code and stores the user connection.
func (s *Server) workerRepositoryCallback(w http.ResponseWriter, r *http.Request) {
	if !s.requireWorkers(w, r) {
		return
	}
	in := struct {
		State string `json:"state"`
		Code  string `json:"code"`
	}{}
	providerDenied := false
	if r.Method == http.MethodGet {
		in.State = r.URL.Query().Get("state")
		in.Code = r.URL.Query().Get("code")
		providerDenied = r.URL.Query().Get("error") != ""
	} else if e := body(r, &in); e != nil {
		s.repositoryCallbackFailure(w, r, e, "")
		return
	}
	if in.State == "" || (in.Code == "" && !providerDenied) {
		s.repositoryCallbackFailure(w, r, problem(400, "INVALID_REQUEST", "OAuth state and code are required"), "")
		return
	}
	browser := digest(s.cookie(r, "browser"))
	var pending WorkerRepositoryConnectState
	var consumed bool
	var e error
	if s.Store.pool == nil {
		e = s.Store.Update(r.Context(), PartitionWorkers, func(*State) error {
			pending, consumed, e = s.Store.consumeRepositoryConnectState(r.Context(), in.State, browser)
			return e
		})
	} else {
		pending, consumed, e = s.Store.consumeRepositoryConnectState(r.Context(), in.State, browser)
	}
	if e != nil {
		s.repositoryCallbackFailure(w, r, e, "")
		return
	}
	if !consumed {
		s.repositoryCallbackFailure(w, r, problem(403, "WORKER_REPOSITORY_STATE_INVALID", "Connect request expired; try again"), "")
		return
	}
	if providerDenied {
		s.recordRepositoryAudit(r, pending, "workers.repository.connect.failed", false)
		s.repositoryCallbackFailure(w, r, problem(400, "WORKER_REPOSITORY_OAUTH_DENIED", "Provider rejected the connection"), pending.Organization)
		return
	}
	if pending.Provider != r.PathValue("provider") {
		s.recordRepositoryAudit(r, pending, "workers.repository.connect.failed", false)
		s.repositoryCallbackFailure(w, r, problem(403, "WORKER_REPOSITORY_STATE_INVALID", "Connect request expired; try again"), pending.Organization)
		return
	}
	provider, e := s.repositoryProvider(pending.Provider, pending.Organization)
	if e != nil {
		s.recordRepositoryAudit(r, pending, "workers.repository.connect.failed", false)
		s.repositoryCallbackFailure(w, r, e, pending.Organization)
		return
	}
	conf := s.repositoryOAuthConfig(provider)
	if conf == nil {
		s.recordRepositoryAudit(r, pending, "workers.repository.connect.failed", false)
		s.repositoryCallbackFailure(w, r, problem(409, "WORKER_REPOSITORY_OAUTH_DISABLED", "This provider is not configured for user connections"), pending.Organization)
		return
	}
	// The provider call is intentionally outside Store.Update: a slow OAuth
	// server must never hold or conceptually extend a database mutation.
	oauthContext, cancel, contextErr := s.repositoryOAuthContext(r.Context(), provider)
	if contextErr != nil {
		s.recordRepositoryAudit(r, pending, "workers.repository.connect.failed", false)
		s.repositoryCallbackFailure(w, r, problem(503, "WORKER_REPOSITORY_OAUTH_UNAVAILABLE", "Repository authorization is unavailable"), pending.Organization)
		return
	}
	defer cancel()
	token, e := conf.Exchange(oauthContext, in.Code)
	if e != nil {
		s.recordRepositoryAudit(r, pending, "workers.repository.connect.failed", false)
		s.repositoryCallbackFailure(w, r, problem(400, "WORKER_REPOSITORY_OAUTH_DENIED", "Provider rejected the connection"), pending.Organization)
		return
	}
	connection := WorkerRepositoryConnection{
		User: pending.User, Provider: pending.Provider,
		AccessToken: token.AccessToken, TokenType: token.TokenType,
		RefreshToken: token.RefreshToken, ExpiresAt: token.Expiry,
		ConnectedAt: now(), RefreshState: "ready",
	}
	audit := Audit{ID: randomID("audit"), Time: now(), Actor: pending.User, Credential: "session", Action: "workers.repository.connect", Resource: pending.Provider, Organization: pending.Organization, Allowed: true, RequestID: r.Header.Get("X-Krun-Request-ID")}
	if s.Store.pool == nil {
		e = s.Store.Update(r.Context(), PartitionWorkers, func(*State) error {
			if err := s.Store.upsertRepositoryConnection(r.Context(), connection); err != nil {
				return err
			}
			s.Store.memState().recordAudit(audit)
			return nil
		})
	} else {
		e = s.Store.upsertRepositoryConnectionAudited(r.Context(), connection, []Audit{audit})
	}
	if e != nil {
		s.repositoryCallbackFailure(w, r, e, pending.Organization)
		return
	}
	if r.Method == http.MethodGet {
		query := url.Values{"page": {"workers"}, "workspace": {pending.Organization}, "repository_status": {"connected"}, "repository_provider": {connection.Provider}}
		http.Redirect(w, r, "/?"+query.Encode(), http.StatusSeeOther)
		return
	}
	s.json(w, 200, map[string]any{"provider": connection.Provider, "connected": true})
}

func (s *Server) repositoryCallbackFailure(w http.ResponseWriter, r *http.Request, err error, workspace string) {
	if r.Method != http.MethodGet {
		s.writeError(w, r, err)
		return
	}
	code := "AUTHORIZATION_UNAVAILABLE"
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		code = apiErr.Code
	}
	query := url.Values{"page": {"workers"}, "repository_status": {"error"}, "error": {code}, "request_id": {r.Header.Get("X-Krun-Request-ID")}}
	if workspace != "" {
		query.Set("workspace", workspace)
	}
	http.Redirect(w, r, "/?"+query.Encode(), http.StatusSeeOther)
}

func (s *Server) workerRepositoryDisconnect(st *State, p Principal, r *http.Request) (any, int, error) {
	target, err := s.orgTarget(st, r)
	if err != nil {
		return nil, 0, err
	}
	if err = s.require(st, p, r, "service.list", target); err != nil {
		return nil, 0, err
	}
	providerID := r.PathValue("provider")
	if _, err = s.repositoryProvider(providerID, r.PathValue("org")); err != nil {
		return nil, 0, err
	}
	st.recordAudit(s.repositoryAudit(p, r, "workers.repository.disconnect", providerID, target.ID, true))
	if s.Store.pool != nil {
		audits := append([]Audit(nil), st.pendingAudits...)
		st.pendingAudits = nil
		err = s.Store.deleteRepositoryConnectionAudited(r.Context(), p.User.ID, providerID, audits)
	} else {
		err = s.Store.deleteRepositoryConnection(r.Context(), p.User.ID, providerID)
	}
	if err != nil {
		return nil, 0, err
	}
	return nil, 204, nil
}

func (s *Server) repositoryAudit(p Principal, r *http.Request, action, provider, organization string, allowed bool) Audit {
	keyID := ""
	if p.Key != nil {
		keyID = p.Key.ID
	}
	return Audit{ID: randomID("audit"), Time: now(), Actor: p.User.ID, Credential: p.Kind, KeyID: keyID, Action: action, Resource: provider, Organization: organization, Allowed: allowed, RequestID: r.Header.Get("X-Krun-Request-ID")}
}

func (s *Server) recordRepositoryAudit(r *http.Request, pending WorkerRepositoryConnectState, action string, allowed bool) {
	_ = s.Store.appendAudits(r.Context(), []Audit{{ID: randomID("audit"), Time: now(), Actor: pending.User, Credential: "session", Action: action, Resource: pending.Provider, Organization: pending.Organization, Allowed: allowed, RequestID: r.Header.Get("X-Krun-Request-ID")}})
}

type repositoryConnectionInfo struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Name      string `json:"name"`
	Priority  int    `json:"priority"`
	BaseURL   string `json:"base_url"`
	Connected bool   `json:"connected"`
	OAuth     bool   `json:"oauth"` // user-level connections available
}

func (s *Server) visibleRepositoryProviders(organization string, connected map[string]bool) []repositoryConnectionInfo {
	out := make([]repositoryConnectionInfo, 0)
	for _, p := range s.Config.Workers.RepositoryProviders {
		if !p.Enabled || (len(p.OrganizationIDs) > 0 && !contains(p.OrganizationIDs, organization)) {
			continue
		}
		info := repositoryConnectionInfo{ID: p.ID, Type: p.Type, Name: p.Name, Priority: p.Priority, BaseURL: p.BaseURL, OAuth: p.ClientID != ""}
		info.Connected = connected[p.ID]
		out = append(out, info)
	}
	// stable order: priority then id
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && (out[j].Priority < out[j-1].Priority || (out[j].Priority == out[j-1].Priority && out[j].ID < out[j-1].ID)); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
