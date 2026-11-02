package control

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	repooauth "github.com/krun-dev/krun/internal/adapters/repositoryoauth"
	appconfig "github.com/krun-dev/krun/internal/config"
	repositories "github.com/krun-dev/krun/internal/repository"
	"golang.org/x/oauth2"
)

func (s *Server) repositoryOAuthConfig(p appconfig.WorkerRepositoryProvider) *oauth2.Config {
	return (&repooauth.Client{Config: &s.Config}).RepositoryOAuthConfig(p)
}

func (s *Server) repositoryOAuthContext(parent context.Context, provider appconfig.WorkerRepositoryProvider) (context.Context, context.CancelFunc, error) {
	return (&repooauth.Client{Config: &s.Config}).RepositoryOAuthContext(parent, provider)
}

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
	url, err := s.repositoryApplication().StartConnection(r.Context(), st, p, target.ID, r.PathValue("provider"), digest(s.cookie(r, "browser")), r.Header.Get("X-Krun-Request-ID"))
	return map[string]string{"authorization_url": url}, 200, err
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
	connection, err := s.repositoryApplication().CompleteConnection(r.Context(), repositories.ConnectInput{State: in.State, Code: in.Code, Provider: r.PathValue("provider"), Browser: digest(s.cookie(r, "browser")), RequestID: r.Header.Get("X-Krun-Request-ID"), Denied: providerDenied})
	if err != nil {
		s.repositoryCallbackFailure(w, r, err, connection.Organization)
		return
	}
	if r.Method == http.MethodGet {
		query := url.Values{"page": {"workers"}, "workspace": {connection.Organization}, "repository_status": {"connected"}, "repository_provider": {connection.Provider}}
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
	err = s.repositoryApplication().Disconnect(r.Context(), st, p, target.ID, r.PathValue("provider"), r.Header.Get("X-Krun-Request-ID"))
	return nil, 204, err
}

func (s *Server) repositoryAudit(p Principal, r *http.Request, action, provider, organization string, allowed bool) Audit {
	return repositories.AuditEntry(p, r.Header.Get("X-Krun-Request-ID"), action, provider, organization, allowed)
}

func (s *Server) recordRepositoryAudit(r *http.Request, pending WorkerRepositoryConnectState, action string, allowed bool) {
	s.repositoryApplication().RecordAudit(r.Context(), pending, r.Header.Get("X-Krun-Request-ID"), action, allowed)
}

func (s *Server) visibleRepositoryProviders(organization string, connected map[string]bool) []repositories.RepositoryConnectionInfo {
	return s.repositoryApplication().VisibleRepositoryProviders(organization, connected)
}
