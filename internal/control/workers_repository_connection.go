package control

import (
	"context"
	"crypto/subtle"
	"io"
	"net/http"
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
	return &oauth2.Config{ClientID: p.ClientID, ClientSecret: p.ClientSecret, Endpoint: endpoint, Scopes: []string{"repo"}, RedirectURL: s.Config.PublicURL + "/api/v1/workers/repository/" + p.ID + "/callback"}
}

// workerRepositoryConnect starts a connection: returns the provider's
// authorization URL with CSRF state bound to this user and browser.
func (s *Server) workerRepositoryConnect(st *State, p Principal, r *http.Request) (any, int, error) {
	provider, err := s.repositoryProvider(r.PathValue("provider"), r.PathValue("org"))
	if err != nil {
		return nil, 0, err
	}
	conf := s.repositoryOAuthConfig(provider)
	if conf == nil {
		return nil, 0, problem(409, "WORKER_REPOSITORY_OAUTH_DISABLED", "This provider is not configured for user connections")
	}
	state := randomToken()
	st.RepositoryConnectStates[state] = WorkerRepositoryConnectState{User: p.User.ID, Provider: provider.ID, Browser: digest(s.cookie(r, "browser")), Expires: now().Add(10 * time.Minute)}
	return map[string]string{"authorization_url": conf.AuthCodeURL(state)}, 200, nil
}

// workerRepositoryCallback exchanges the OAuth code and stores the user connection.
func (s *Server) workerRepositoryCallback(w http.ResponseWriter, r *http.Request) {
	var in struct {
		State string `json:"state"`
		Code  string `json:"code"`
	}
	if e := body(r, &in); e != nil {
		s.writeError(w, r, e)
		return
	}
	var connection WorkerRepositoryConnection
	e := s.Store.Update(r.Context(), func(st *State) error {
		pending, ok := st.RepositoryConnectStates[in.State]
		if !ok || now().After(pending.Expires) {
			return problem(403, "WORKER_REPOSITORY_STATE_INVALID", "Connect request expired; try again")
		}
		delete(st.RepositoryConnectStates, in.State)
		if subtle.ConstantTimeCompare([]byte(pending.Browser), []byte(digest(s.cookie(r, "browser")))) != 1 {
			return problem(403, "WORKER_REPOSITORY_STATE_INVALID", "Connect request expired; try again")
		}
		provider, err := s.repositoryProvider(pending.Provider, "")
		if err != nil {
			return err
		}
		conf := s.repositoryOAuthConfig(provider)
		if conf == nil {
			return problem(409, "WORKER_REPOSITORY_OAUTH_DISABLED", "This provider is not configured for user connections")
		}
		token, e := conf.Exchange(r.Context(), in.Code)
		if e != nil {
			return problem(402, "WORKER_REPOSITORY_OAUTH_DENIED", "Provider rejected the connection")
		}
		if st.RepositoryConnections == nil {
			st.RepositoryConnections = map[string]WorkerRepositoryConnection{}
		}
		connection = WorkerRepositoryConnection{User: pending.User, Provider: pending.Provider, AccessToken: token.AccessToken, ConnectedAt: now()}
		st.RepositoryConnections[pending.User+"\x00"+pending.Provider] = connection
		st.Revision++
		return nil
	})
	if e != nil {
		s.writeError(w, r, e)
		return
	}
	s.json(w, 200, map[string]any{"provider": connection.Provider, "connected": true})
}

func (s *Server) workerRepositoryDisconnect(st *State, p Principal, r *http.Request) (any, int, error) {
	providerID := r.PathValue("provider")
	if _, err := s.repositoryProvider(providerID, r.PathValue("org")); err != nil {
		return nil, 0, err
	}
	delete(st.RepositoryConnections, p.User.ID+"\x00"+providerID)
	st.Revision++
	return nil, 204, nil
}

// verifyRepositoryConnection checks the stored token still works; used by the
// providers listing so the UI can surface revoked grants.
func (s *Server) verifyRepositoryConnection(ctx context.Context, provider WorkerRepositoryProvider, token string) bool {
	if token == "" {
		return false
	}
	target := repositoryAPIOrigin(provider) + "/user"
	if provider.Type == "gitlab" {
		target = provider.BaseURL + "/api/v4/user"
	}
	resp, err := s.repositoryRequestWithToken(ctx, provider, target, token)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<10))
	return resp.StatusCode == http.StatusOK
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

// visibleRepositoryProviders runs inside the request's state transaction (st);
// it must not open a nested Store.Update.
func (s *Server) visibleRepositoryProviders(st *State, organization string, userID string) []repositoryConnectionInfo {
	out := make([]repositoryConnectionInfo, 0)
	for _, p := range s.Config.Workers.RepositoryProviders {
		if !p.Enabled || (len(p.OrganizationIDs) > 0 && !contains(p.OrganizationIDs, organization)) {
			continue
		}
		info := repositoryConnectionInfo{ID: p.ID, Type: p.Type, Name: p.Name, Priority: p.Priority, BaseURL: p.BaseURL, OAuth: p.ClientID != ""}
		if st != nil && userID != "" {
			if c, ok := st.RepositoryConnections[userID+"\x00"+p.ID]; ok && c.AccessToken != "" {
				info.Connected = true
			}
		}
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
