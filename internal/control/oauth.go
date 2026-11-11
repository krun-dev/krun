package control

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	authapp "github.com/krun-dev/krun/internal/auth"
)

const oauthPrefix = "/api/auth/oauth"

func (s *Server) oauthRoutes(m *recordingMux) {
	m.useScope(scopeNone)
	m.HandleFunc("GET "+oauthPrefix+"/authorize", s.oauthAuthorize)
	m.HandleFunc("POST "+oauthPrefix+"/device/auth", s.oauthDeviceStart)
	m.HandleFunc("GET "+oauthPrefix+"/requests/{request}", s.oauthRequest)
	m.HandleFunc("POST "+oauthPrefix+"/requests/{request}", s.oauthDecide)
	m.HandleFunc("POST "+oauthPrefix+"/token", s.oauthToken)
	m.HandleFunc("POST "+oauthPrefix+"/revoke", s.oauthRevoke)
}
func (s *Server) oauthError(w http.ResponseWriter, r *http.Request, err error) {
	var oauth *authapp.OAuthError
	if errors.As(err, &oauth) {
		status := 400
		s.json(w, status, map[string]string{"error": oauth.Code, "error_description": oauth.Description})
		return
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		s.json(w, apiErr.Status, map[string]string{"error": "invalid_request", "error_description": apiErr.Message})
		return
	}
	s.json(w, 503, map[string]string{"error": "temporarily_unavailable", "error_description": "Authorization service unavailable"})
}
func (s *Server) oauthForm(w http.ResponseWriter, r *http.Request) (url.Values, error) {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		return nil, problem(415, "INVALID_REQUEST", "application/x-www-form-urlencoded required")
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		return nil, problem(400, "INVALID_REQUEST", "Invalid OAuth form")
	}
	for _, values := range r.PostForm {
		if len(values) != 1 {
			return nil, problem(400, "INVALID_REQUEST", "Repeated OAuth form parameter")
		}
	}
	return r.PostForm, nil
}
func (s *Server) oauthAuthorize(w http.ResponseWriter, r *http.Request) {
	if err := s.checkRateLimit(r); err != nil {
		s.oauthError(w, r, err)
		return
	}
	q := r.URL.Query()
	for _, v := range q {
		if len(v) != 1 {
			s.oauthError(w, r, problem(400, "INVALID_REQUEST", "Repeated authorization parameter"))
			return
		}
	}
	if q.Get("response_mode") != "" && q.Get("response_mode") != "query" {
		s.oauthError(w, r, problem(400, "INVALID_REQUEST", "Only query response mode is supported"))
		return
	}
	id, _, err := s.authApplication().StartOAuth(r.Context(), authapp.OAuthAuthorizeInput{Client: q.Get("client_id"), Redirect: q.Get("redirect_uri"), Challenge: q.Get("code_challenge"), ChallengeMethod: q.Get("code_challenge_method"), State: q.Get("state"), Scope: q.Get("scope"), ResponseType: q.Get("response_type")}, false)
	if err != nil {
		s.oauthError(w, r, err)
		return
	}
	http.Redirect(w, r, s.Config.PublicURL+"/?oauth="+url.QueryEscape(id), http.StatusSeeOther)
}
func (s *Server) oauthDeviceStart(w http.ResponseWriter, r *http.Request) {
	if err := s.checkRateLimit(r); err != nil {
		s.oauthError(w, r, err)
		return
	}
	f, err := s.oauthForm(w, r)
	if err != nil {
		s.oauthError(w, r, err)
		return
	}
	id, a, err := s.authApplication().StartOAuth(r.Context(), authapp.OAuthAuthorizeInput{Client: f.Get("client_id"), Scope: f.Get("scope")}, true)
	if err != nil {
		s.oauthError(w, r, err)
		return
	}
	s.json(w, 200, map[string]any{"device_code": id, "user_code": a.UserCode, "verification_uri": s.Config.PublicURL + "/?oauth_device=1", "verification_uri_complete": s.Config.PublicURL + "/?oauth=" + url.QueryEscape(a.UserCode), "expires_in": 300, "interval": 3})
}
func (s *Server) oauthRequest(w http.ResponseWriter, r *http.Request) {
	if err := s.checkRateLimit(r); err != nil {
		s.writeError(w, r, err)
		return
	}
	a, err := s.authApplication().OAuthRequest(r.Context(), r.PathValue("request"))
	if err != nil {
		var expected *authapp.OAuthError
		if errors.As(err, &expected) {
			s.writeError(w, r, problem(400, "OAUTH_REQUEST_EXPIRED", "Authorization request expired or already used"))
		} else {
			s.writeError(w, r, err)
		}
		return
	}
	callback := ""
	if a.Redirect != "" {
		u, _ := url.Parse(a.Redirect)
		callback = u.Host
	}
	s.json(w, 200, map[string]any{"client_id": a.Client, "application": authapp.OAuthClientName(a.Client), "scopes": strings.Fields(a.Scope), "expires_at": a.Expires, "status": a.Decision, "device_code": a.UserCode, "callback": callback})
}
func (s *Server) oauthDecide(w http.ResponseWriter, r *http.Request) {
	c, err := s.readCredentials(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	r, err = s.scopedRequest(r, c)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var p Principal
	var revision int
	err = s.Store.View(r.Context(), PartitionIAM, func(st *State) error {
		var e error
		p, e = s.protectedPrincipal(st, c, r, true)
		revision = st.Revision
		return e
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if p.Kind != "session" {
		s.writeError(w, r, forbidden())
		return
	}
	if err = s.authApplication().CheckDeviceApproval(r.Context(), p, s.clientIP(r)); err != nil {
		s.writeError(w, r, err)
		return
	}
	var in struct {
		Allow bool `json:"allow"`
	}
	if err = body(r, &in); err != nil {
		s.writeError(w, r, err)
		return
	}
	target, err := s.authApplication().DecideOAuth(r.Context(), r.PathValue("request"), p, in.Allow, r.Header.Get("X-Krun-Request-ID"), revision)
	if err != nil {
		var expected *authapp.OAuthError
		if errors.As(err, &expected) {
			s.writeError(w, r, problem(400, "OAUTH_REQUEST_EXPIRED", "Authorization request expired or already used"))
		} else {
			s.writeError(w, r, err)
		}
		return
	}
	s.json(w, 200, map[string]string{"redirect_url": target, "status": map[bool]string{true: "approved", false: "denied"}[in.Allow]})
}
func (s *Server) oauthToken(w http.ResponseWriter, r *http.Request) {
	if err := s.checkRateLimit(r); err != nil {
		s.oauthError(w, r, err)
		return
	}
	f, err := s.oauthForm(w, r)
	if err != nil {
		s.oauthError(w, r, err)
		return
	}
	out, err := s.authApplication().ExchangeOAuth(r.Context(), authapp.OAuthTokenInput{GrantType: f.Get("grant_type"), Client: f.Get("client_id"), Code: f.Get("code"), Redirect: f.Get("redirect_uri"), Verifier: f.Get("code_verifier"), Refresh: f.Get("refresh_token"), Device: f.Get("device_code"), Scope: f.Get("scope")})
	if err != nil {
		s.oauthError(w, r, err)
		return
	}
	s.json(w, 200, out)
}
func (s *Server) oauthRevoke(w http.ResponseWriter, r *http.Request) {
	if err := s.checkRateLimit(r); err != nil {
		s.oauthError(w, r, err)
		return
	}
	f, err := s.oauthForm(w, r)
	if err != nil {
		s.oauthError(w, r, err)
		return
	}
	if err = s.authApplication().RevokeOAuth(r.Context(), f.Get("client_id"), f.Get("token")); err != nil {
		s.oauthError(w, r, err)
		return
	}
	s.json(w, 200, map[string]any{})
}
