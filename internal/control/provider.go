package control

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	identityprovider "github.com/krun-dev/krun/internal/adapters/identity"
	authapp "github.com/krun-dev/krun/internal/auth"
	"github.com/krun-dev/krun/internal/localize"
)

func loginReturnPath(value string) (string, error) {
	if value == "" {
		return "/", nil
	}
	u, err := url.Parse(value)
	if err != nil || u.IsAbs() || u.Host != "" || u.Path != "/" || u.RawPath != "" || u.Fragment != "" || strings.ContainsAny(value, "\\\r\n") {
		return "", problem(400, "INVALID_REQUEST", "Invalid return path")
	}
	return value, nil
}

func (s *Server) loginHandler(w http.ResponseWriter, r *http.Request) {
	if err := s.checkRateLimit(r); err != nil {
		s.writeError(w, r, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if e := r.ParseForm(); e != nil {
		s.writeError(w, r, problem(400, "INVALID_REQUEST", "Invalid login form"))
		return
	}
	p, ok := s.providers[r.PathValue("provider")]
	if !ok || p.Config.Type == "email" {
		s.writeError(w, r, problem(404, "AUTH_PROVIDER_UNAVAILABLE", "Provider unavailable"))
		return
	}
	returnTo, e := loginReturnPath(r.Form.Get("return_to"))
	if e != nil {
		s.writeError(w, r, e)
		return
	}
	browserHash := digest(s.cookie(r, "browser"))
	browser, ok, e := s.Store.BrowserFor(r.Context(), browserHash)
	if e != nil {
		s.writeError(w, r, e)
		return
	}
	if !ok || !now().Before(browser.Expires) || !s.validCSRF(r, browser.CSRF) {
		s.writeError(w, r, problem(403, "CSRF_INVALID", "Login CSRF verification failed"))
		return
	}
	challenge, err := s.authApplication().BeginLogin(r.Context(), p.Config, browserHash, browser, returnTo)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	redirect := p.Begin(challenge.State, challenge.Verifier, challenge.Nonce)
	if r.Header.Get("Accept") == "application/json" {
		s.json(w, 200, map[string]string{"redirect_url": redirect})
		return
	}
	http.Redirect(w, r, redirect, http.StatusSeeOther)
}

func (s *Server) callbackHandler(w http.ResponseWriter, r *http.Request) {
	if err := s.checkRateLimit(r); err != nil {
		var apiError *APIError
		if errors.As(err, &apiError) && apiError.Code == "RATE_LIMITED" {
			s.callbackRedirect(w, r, "RATE_LIMITED")
		} else {
			s.writeError(w, r, err)
		}
		return
	}
	p, ok := s.providers[r.PathValue("provider")]
	if !ok || p.Config.Type == "email" {
		s.callbackError(w, r, "AUTH_PROVIDER_UNAVAILABLE")
		return
	}
	login, e := s.authApplication().ConsumeLogin(r.Context(), r.URL.Query().Get("state"), digest(s.cookie(r, "browser")), p.Config)
	if e != nil {
		s.callbackError(w, r, "AUTH_STATE_INVALID")
		return
	}
	tx := login
	if r.URL.Query().Get("error") != "" || r.URL.Query().Get("code") == "" {
		s.callbackError(w, r, "AUTH_UPSTREAM_DENIED")
		return
	}
	identity, e := p.Complete(r.Context(), r.URL.Query().Get("code"), tx.Verifier, tx.Nonce)
	if e != nil {
		failure := &identityprovider.ProviderFailure{Stage: "identity", Reason: "invalid_upstream_response"}
		var diagnostic *identityprovider.ProviderFailure
		if errors.As(e, &diagnostic) {
			failure = diagnostic
		}
		slog.Warn(localize.Text(localize.RequestLanguage(r.Header.Get("Accept-Language")), "SSO upstream failed"), "provider", p.Config.ID, "request_id", r.Header.Get("X-Krun-Request-ID"), "stage", failure.Stage, "reason", failure.Reason, "http_status", failure.Status)
		s.callbackError(w, r, "AUTH_UPSTREAM_FAILED")
		return
	}
	sessionToken := randomToken()
	// The browser generation may have moved while the upstream call was in
	// flight; a superseded login must not create a session.
	if !s.authApplication().LoginCurrent(r.Context(), tx) {
		s.callbackError(w, r, "AUTH_STATE_INVALID")
		return
	}

	if e := s.finishLogin(r.Context(), r, p, tx.Browser, tx.Generation, identity, sessionToken); e != nil {
		code := "AUTH_UPSTREAM_FAILED"
		var a *APIError
		if errors.As(e, &a) {
			code = a.Code
		}
		s.callbackError(w, r, code)
		return
	}
	s.setCookie(w, "session", sessionToken, 8*3600)
	http.Redirect(w, r, tx.ReturnTo, http.StatusSeeOther)
}

// finishLogin creates or refreshes the user and issues a session. The user,
// invitation, session, browser-generation CAS and audit are committed by the
// IAM store in one small transaction.
func (s *Server) finishLogin(ctx context.Context, r *http.Request, p *identityprovider.Provider, browserHash string, expectedGeneration int, id identityprovider.Identity, sessionToken string) error {
	return s.finishLoginCommit(ctx, r, p, browserHash, expectedGeneration, id, sessionToken, "")
}

func (s *Server) finishEmailLogin(ctx context.Context, r *http.Request, p *identityprovider.Provider, browserHash string, expectedGeneration int, id identityprovider.Identity, sessionToken, challengeHash string) error {
	return s.finishLoginCommit(ctx, r, p, browserHash, expectedGeneration, id, sessionToken, challengeHash)
}

func (s *Server) finishLoginCommit(ctx context.Context, r *http.Request, p *identityprovider.Provider, browserHash string, expectedGeneration int, id identityprovider.Identity, sessionToken, challengeHash string) error {
	oldSessionHash := ""
	if previous := s.cookie(r, "session"); previous != "" {
		oldSessionHash = digest(previous)
	}
	return s.authApplication().CommitLogin(ctx, authapp.LoginInput{Provider: p.Config, Identity: id, BrowserHash: browserHash, ExpectedGeneration: expectedGeneration, SessionToken: sessionToken, OldSessionHash: oldSessionHash, ChallengeHash: challengeHash, RequestID: r.Header.Get("X-Krun-Request-ID")})
}

func (s *Server) callbackError(w http.ResponseWriter, r *http.Request, code string) {
	// Callback failures are attacker-controlled read traffic. Persisting one
	// audit row per failure lets invalid or rate-limited requests amplify writes;
	// successful login remains durably audited in finishLogin.
	s.callbackRedirect(w, r, code)
}

func (s *Server) callbackRedirect(w http.ResponseWriter, r *http.Request, code string) {
	http.Redirect(w, r, "/?error="+url.QueryEscape(code)+"&request_id="+url.QueryEscape(r.Header.Get("X-Krun-Request-ID")), http.StatusSeeOther)
}

func (s *Server) deviceStart(w http.ResponseWriter, r *http.Request) {
	if err := s.checkRateLimit(r); err != nil {
		s.writeError(w, r, err)
		return
	}
	var input struct{}
	if e := body(r, &input); e != nil {
		s.writeError(w, r, e)
		return
	}
	device, user, err := s.authApplication().StartDevice(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.json(w, 200, map[string]any{"device_code": device, "user_code": user, "verification_uri": s.Config.PublicURL + "/?cli=" + user, "expires_in": 300, "interval": 3})
}

func (s *Server) devicePoll(w http.ResponseWriter, r *http.Request) {
	if err := s.checkRateLimit(r); err != nil {
		s.writeError(w, r, err)
		return
	}
	var input struct {
		Code string `json:"device_code"`
	}
	if e := body(r, &input); e != nil {
		s.writeError(w, r, e)
		return
	}
	token, err := s.authApplication().PollDevice(r.Context(), input.Code)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.json(w, 200, map[string]any{"token": token, "expires_in": 3600, "token_type": "Bearer", "audience": "krun-control-api"})
}

func (s *Server) deviceApprove(st *State, p Principal, r *http.Request) (any, int, error) {
	if err := s.authApplication().CheckDeviceApproval(r.Context(), p, s.clientIP(r)); err != nil {
		return nil, 0, err
	}
	var input struct {
		Code string `json:"user_code"`
	}
	if err := body(r, &input); err != nil {
		return nil, 0, err
	}
	if err := s.authApplication().ApproveDevice(r.Context(), st, p, input.Code, r.Header.Get("X-Krun-Request-ID")); err != nil {
		return nil, 0, err
	}
	return map[string]string{"status": "approved"}, 200, nil
}
