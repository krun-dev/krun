package control

import (
	"net/http"

	identityprovider "github.com/krun-dev/krun/internal/adapters/identity"
	authapp "github.com/krun-dev/krun/internal/auth"
	appconfig "github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/localize"
)

func emailUnavailable() error {
	return problem(404, "AUTH_PROVIDER_UNAVAILABLE", "Provider unavailable")
}

func (s *Server) emailCodeHandler(w http.ResponseWriter, r *http.Request) {
	p, ok := s.providers[r.PathValue("provider")]
	if !ok || p.Config.Type != "email" {
		s.writeError(w, r, emailUnavailable())
		return
	}
	if err := s.checkRateLimit(r); err != nil {
		s.writeError(w, r, err)
		return
	}
	var input struct {
		Email    string `json:"email"`
		ReturnTo string `json:"return_to"`
	}
	if err := body(r, &input); err != nil {
		s.writeError(w, r, err)
		return
	}
	email, err := appconfig.NormalizedEmail(input.Email)
	if err != nil {
		s.writeError(w, r, problem(400, "INVALID_REQUEST", err.Error()))
		return
	}
	returnTo, err := loginReturnPath(input.ReturnTo)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	browserHash := digest(s.cookie(r, "browser"))
	ip := s.clientIP(r)
	browser, ok, err := s.Store.BrowserFor(r.Context(), browserHash)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if !ok || !now().Before(browser.Expires) || !s.validCSRF(r, browser.CSRF) {
		s.writeError(w, r, problem(403, "CSRF_INVALID", "Login CSRF verification failed"))
		return
	}
	id, err := s.authApplication().IssueEmail(r.Context(), authapp.EmailChallengeInput{Provider: p.Config, Email: email, ReturnTo: returnTo, BrowserHash: browserHash, IP: ip, Language: localize.RequestLanguage(r.Header.Get("Accept-Language")), Browser: browser}, s.sendEmail)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.json(w, 200, map[string]any{"challenge_id": id, "expires_in": 600, "retry_after": 60})
}

func (s *Server) emailVerifyHandler(w http.ResponseWriter, r *http.Request) {
	p, ok := s.providers[r.PathValue("provider")]
	if !ok || p.Config.Type != "email" {
		s.writeError(w, r, emailUnavailable())
		return
	}
	if err := s.checkRateLimit(r); err != nil {
		s.writeError(w, r, err)
		return
	}
	var input struct {
		Challenge string `json:"challenge_id"`
		Code      string `json:"code"`
	}
	if err := body(r, &input); err != nil {
		s.writeError(w, r, err)
		return
	}
	sessionToken := randomToken()
	browserHash := digest(s.cookie(r, "browser"))
	browser, hasBrowser, err := s.Store.BrowserFor(r.Context(), browserHash)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if !hasBrowser || !now().Before(browser.Expires) || !s.validCSRF(r, browser.CSRF) {
		s.writeError(w, r, problem(403, "CSRF_INVALID", "Login CSRF verification failed"))
		return
	}
	key := digest(input.Challenge)
	challenge, err := s.authApplication().VerifyEmail(r.Context(), authapp.EmailVerifyInput{Provider: p.Config, Challenge: input.Challenge, Code: input.Code, BrowserHash: browserHash, RequestID: r.Header.Get("X-Krun-Request-ID"), Browser: browser})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if e := s.finishEmailLogin(r.Context(), r, p, browserHash, challenge.Generation, identityprovider.Identity{Subject: challenge.Email, Name: challenge.Email, Email: challenge.Email, EmailVerified: true}, sessionToken, key); e != nil {
		s.recordAnonymousAudit(r, p, "auth.login.failed."+e.Error())
		s.writeError(w, r, e)
		return
	}
	s.setCookie(w, "session", sessionToken, 8*3600)
	s.json(w, 200, map[string]string{"redirect_url": challenge.ReturnTo})
}

// recordAnonymousAudit commits a denial entry without a state mutation.
func (s *Server) recordAnonymousAudit(r *http.Request, p *identityprovider.Provider, action string) {
	s.authApplication().RecordAnonymous(r.Context(), p.Config.ID, r.Header.Get("X-Krun-Request-ID"), action)
}
