package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/krun-dev/krun/internal/configfile"
	"github.com/krun-dev/krun/internal/localize"
	"golang.org/x/oauth2"
)

type provider struct {
	config ProviderConfig
	oauth  *oauth2.Config
	oidc   *oidc.Provider
	client *http.Client
}
type identity struct{ Subject, Name, Email string }

var errUpstreamRedirect = errors.New("upstream redirects disabled")

// Keep diagnostics independent of upstream bodies, URLs and credential values.
type providerFailure struct {
	Stage, Reason string
	Status        int
}

func (e *providerFailure) Error() string { return e.Stage + ": " + e.Reason }
func providerFailed(stage, reason string, status int) error {
	return &providerFailure{Stage: stage, Reason: reason, Status: status}
}

// Requests go only to configured hosts. Redirects are refused before forwarding credentials.
func upstreamClient(c Config) *http.Client {
	allowed := map[string]bool{}
	for _, p := range c.Providers {
		for _, endpoint := range []string{p.AuthorizeURL, p.TokenURL, p.UserInfoURL, p.Issuer} {
			u, _ := url.Parse(endpoint)
			if u != nil {
				allowed[u.Hostname()] = true
			}
		}
	}
	transport := &http.Transport{TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 5 * time.Second, MaxResponseHeaderBytes: 32 << 10, Proxy: nil, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, e := net.SplitHostPort(address)
		if e != nil || !allowed[host] {
			return nil, errors.New("upstream host not allowed")
		}
		ips, e := net.DefaultResolver.LookupIPAddr(ctx, host)
		if e != nil {
			return nil, e
		}
		for _, ip := range ips {
			if ip.IP.IsLoopback() || ip.IP.IsUnspecified() || ip.IP.IsLinkLocalUnicast() || ip.IP.IsLinkLocalMulticast() {
				return nil, errors.New("unsafe upstream address")
			}
		}
		if len(ips) == 0 {
			return nil, errors.New("no upstream addresses")
		}
		d := net.Dialer{Timeout: 5 * time.Second}
		return d.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
	}}
	return &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errUpstreamRedirect }}
}
func newProvider(ctx context.Context, c Config, p ProviderConfig, client *http.Client) (*provider, error) {
	out := &provider{config: p, client: client}
	if p.Type == "dev" || p.Type == "email" {
		return out, nil
	}
	endpoint := oauth2.Endpoint{AuthURL: p.AuthorizeURL, TokenURL: p.TokenURL}
	if p.Type == "oidc" {
		ctx = oidc.ClientContext(ctx, client)
		idp, e := oidc.NewProvider(ctx, p.Issuer)
		if e != nil {
			return nil, fmt.Errorf("OIDC discovery failed for %s", p.ID)
		}
		endpoint = idp.Endpoint()
		for _, endpointURL := range []string{endpoint.AuthURL, endpoint.TokenURL} {
			u, e := url.Parse(endpointURL)
			issuer, _ := url.Parse(p.Issuer)
			if e != nil || u.Scheme != "https" || u.Host != issuer.Host {
				return nil, errors.New("OIDC endpoints must be HTTPS on configured issuer host")
			}
		}
		out.oidc = idp

	}
	if p.TokenAuth == "client_secret_basic" {
		endpoint.AuthStyle = oauth2.AuthStyleInHeader
	} else {
		endpoint.AuthStyle = oauth2.AuthStyleInParams
	}
	out.oauth = &oauth2.Config{ClientID: p.ClientID, ClientSecret: p.ClientSecret, Endpoint: endpoint, Scopes: p.Scopes, RedirectURL: c.PublicURL + "/api/auth/" + p.ID + "/callback"}
	return out, nil
}
func (p *provider) begin(state, verifier, nonce string) string {
	if p.config.Type == "dev" {
		return "/api/auth/" + p.config.ID + "/callback?state=" + url.QueryEscape(state) + "&code=dev"
	}
	options := []oauth2.AuthCodeOption{oauth2.S256ChallengeOption(verifier)}
	for name, value := range p.config.AuthorizeParams {
		options = append(options, oauth2.SetAuthURLParam(name, value))
	}
	if p.oidc != nil {
		options = append(options, oauth2.SetAuthURLParam("nonce", nonce))
	}
	return p.oauth.AuthCodeURL(state, options...)
}
func (p *provider) complete(ctx context.Context, code, verifier, nonce string) (identity, error) {
	if p.config.Type == "dev" {
		if code != "dev" {
			return identity{}, errors.New("invalid dev code")
		}
		return identity{Subject: "developer", Name: "本地开发用户"}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	ctx = context.WithValue(ctx, oauth2.HTTPClient, p.client)
	token, e := p.oauth.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if e != nil {
		status := 0
		reason := "request_failed"
		var response *oauth2.RetrieveError
		if errors.As(e, &response) {
			if response.Response != nil {
				status = response.Response.StatusCode
			}
			switch response.ErrorCode {
			case "invalid_client", "invalid_grant", "invalid_scope", "invalid_request", "unauthorized_client", "unsupported_grant_type":
				reason = response.ErrorCode
			default:
				reason = "token_response_rejected"
			}
		}
		return identity{}, providerFailed("token", reason, status)
	}
	if p.oidc != nil {
		raw, ok := token.Extra("id_token").(string)
		if !ok || len(raw) > 64<<10 {
			return identity{}, errors.New("ID token required")
		}
		verified, e := p.oidc.Verifier(&oidc.Config{ClientID: p.config.ClientID}).Verify(oidc.ClientContext(ctx, p.client), raw)
		if e != nil || verified.Nonce != nonce || verified.Subject == "" || len(verified.Subject) > 512 {
			return identity{}, errors.New("invalid ID token")
		}
		var claims struct {
			Name  string `json:"name"`
			Email string `json:"email"`
		}
		if e = verified.Claims(&claims); e != nil {
			return identity{}, e
		}
		return identity{verified.Subject, claims.Name, claims.Email}, nil
	}
	endpointURL, e := url.Parse(p.config.UserInfoURL)
	if e != nil {
		return identity{}, e
	}
	query := endpointURL.Query()
	for name, value := range p.config.UserInfo.Query {
		query.Set(name, value)
	}
	endpointURL.RawQuery = query.Encode()
	var requestBody io.Reader
	if len(p.config.UserInfo.Body) > 0 {
		raw, e := json.Marshal(p.config.UserInfo.Body)
		if e != nil {
			return identity{}, e
		}
		requestBody = bytes.NewReader(raw)
	}
	method := p.config.UserInfo.Method
	if method == "" {
		method = "GET"
	}
	req, e := http.NewRequestWithContext(ctx, method, endpointURL.String(), requestBody)
	if e != nil {
		return identity{}, e
	}
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "krun-control/0.1")
	for name, value := range p.config.UserInfo.Headers {
		req.Header.Set(name, value)
	}
	if requestBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, e := p.client.Do(req)
	if e != nil {
		reason := "request_failed"
		if errors.Is(e, errUpstreamRedirect) {
			reason = "redirect_rejected"
		}
		var networkError net.Error
		if errors.As(e, &networkError) && networkError.Timeout() {
			reason = "request_timeout"
		}
		return identity{}, providerFailed("userinfo", reason, 0)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return identity{}, providerFailed("userinfo", "http_response_rejected", response.StatusCode)
	}
	raw, e := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if e != nil || len(raw) > 1<<20 {
		return identity{}, errors.New("userinfo too large")
	}
	var data any
	if e = configfile.DecodeJSON(raw, &data); e != nil {
		return identity{}, e
	}
	if success := p.config.UserInfo.Success; success != nil {
		actual, e := lookup(data, success.Path)
		if e != nil || !reflect.DeepEqual(actual, success.Equals) {
			return identity{}, errors.New("userinfo business status failed")
		}
	}
	subject, e := mapped(data, p.config.SubjectPath, true)
	if e != nil || subject == "" || len(subject) > 512 {
		return identity{}, providerFailed("userinfo", "stable_subject_missing_or_invalid", response.StatusCode)
	}
	name, _ := mapped(data, p.config.NamePath, false)
	email, _ := mapped(data, p.config.EmailPath, false)
	return identity{subject, name, email}, nil
}
func mapped(data any, path string, required bool) (string, error) {
	if path == "" {
		if required {
			return "", errors.New("required path")
		}
		return "", nil
	}
	value, e := lookup(data, path)
	if e != nil {
		return "", e
	}

	switch v := value.(type) {
	case string:
		return v, nil
	case json.Number:
		if strings.ContainsAny(string(v), ".eE") {
			return "", errors.New("integer ID required")
		}
		return string(v), nil
	case nil:
		if !required {
			return "", nil
		}
	}
	return "", errors.New("string or integer required")
}
func lookup(data any, path string) (any, error) {
	value := data
	for _, segment := range strings.Split(path, ".") {
		switch object := value.(type) {
		case map[string]any:
			var ok bool
			value, ok = object[segment]
			if !ok {
				return nil, errors.New("field missing")
			}
		case []any:
			index, e := strconv.Atoi(segment)
			if e != nil || index < 0 || index >= len(object) {
				return nil, errors.New("array index invalid")
			}
			value = object[index]
		default:
			return nil, errors.New("invalid field path")
		}
	}
	return value, nil
}
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
	if !ok || p.config.Type == "email" {
		s.writeError(w, r, problem(404, "AUTH_PROVIDER_UNAVAILABLE", "Provider unavailable"))
		return
	}
	returnTo, e := loginReturnPath(r.Form.Get("return_to"))
	if e != nil {
		s.writeError(w, r, e)
		return
	}
	state := randomToken()
	verifier := oauth2.GenerateVerifier()
	nonce := randomToken()
	browserHash := digest(s.cookie(r, "browser"))
	browser, ok, e := s.Store.browserFor(r.Context(), browserHash)
	if e != nil {
		s.writeError(w, r, e)
		return
	}
	if !ok || !now().Before(browser.Expires) || !s.validCSRF(r, browser.CSRF) {
		s.writeError(w, r, problem(403, "CSRF_INVALID", "Login CSRF verification failed"))
		return
	}
	login := Login{Provider: p.config.ID, Browser: browserHash, Fingerprint: loginFingerprint(p.config), Verifier: verifier, Nonce: nonce, ReturnTo: returnTo, Expires: now().Add(5 * time.Minute)}
	if _, e = s.Store.beginLoginState(r.Context(), browserHash, browser, digest(state), login); e != nil {
		s.writeError(w, r, e)
		return
	}
	redirect := p.begin(state, verifier, nonce)
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
	if !ok || p.config.Type == "email" {
		s.callbackError(w, r, "AUTH_PROVIDER_UNAVAILABLE")
		return
	}
	stateHash := digest(r.URL.Query().Get("state"))
	login, ok, e := s.Store.loginState(r.Context(), stateHash)
	if e != nil {
		s.callbackError(w, r, "AUTH_STATE_INVALID")
		return
	}
	browser, hasBrowser, e := s.Store.browserFor(r.Context(), login.Browser)
	if e != nil {
		s.callbackError(w, r, "AUTH_STATE_INVALID")
		return
	}
	browserHash := digest(s.cookie(r, "browser"))
	if !ok || !hasBrowser || login.Consumed || login.Provider != p.config.ID || login.Browser != browserHash || login.Generation != browser.Generation || !now().Before(login.Expires) || login.Fingerprint != loginFingerprint(p.config) {
		s.callbackError(w, r, "AUTH_STATE_INVALID")
		return
	}
	// Consuming the transaction is the single point that makes a replayed
	// callback lose, so it happens before any upstream call.
	if consumed, e := s.Store.consumeLoginState(r.Context(), stateHash); e != nil || !consumed {
		s.callbackError(w, r, "AUTH_STATE_INVALID")
		return
	}
	tx := login
	if r.URL.Query().Get("error") != "" || r.URL.Query().Get("code") == "" {
		s.callbackError(w, r, "AUTH_UPSTREAM_DENIED")
		return
	}
	identity, e := p.complete(r.Context(), r.URL.Query().Get("code"), tx.Verifier, tx.Nonce)
	if e != nil {
		failure := &providerFailure{Stage: "identity", Reason: "invalid_upstream_response"}
		var diagnostic *providerFailure
		if errors.As(e, &diagnostic) {
			failure = diagnostic
		}
		slog.Warn(localize.Text(localize.RequestLanguage(r.Header.Get("Accept-Language")), "SSO upstream failed"), "provider", p.config.ID, "request_id", r.Header.Get("X-Krun-Request-ID"), "stage", failure.Stage, "reason", failure.Reason, "http_status", failure.Status)
		s.callbackError(w, r, "AUTH_UPSTREAM_FAILED")
		return
	}
	sessionToken := randomToken()
	// The browser generation may have moved while the upstream call was in
	// flight; a superseded login must not create a session.
	if current, ok, e := s.Store.browserFor(r.Context(), tx.Browser); e != nil || !ok || current.Generation != tx.Generation || !now().Before(tx.Expires) {
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
func (s *Server) finishLogin(ctx context.Context, r *http.Request, p *provider, browserHash string, expectedGeneration int, id identity, sessionToken string) error {
	return s.finishLoginCommit(ctx, r, p, browserHash, expectedGeneration, id, sessionToken, "")
}

func (s *Server) finishEmailLogin(ctx context.Context, r *http.Request, p *provider, browserHash string, expectedGeneration int, id identity, sessionToken, challengeHash string) error {
	return s.finishLoginCommit(ctx, r, p, browserHash, expectedGeneration, id, sessionToken, challengeHash)
}

func (s *Server) finishLoginCommit(ctx context.Context, r *http.Request, p *provider, browserHash string, expectedGeneration int, id identity, sessionToken, challengeHash string) error {
	at := now()
	session := Session{Hash: digest(sessionToken), CSRF: randomToken(), Created: at, Seen: at, Absolute: at.Add(8 * time.Hour)}
	for attempt := 0; ; attempt++ {
		err := s.Store.Update(ctx, PartitionIAM, func(st *State) error {
			if s.Store.pool == nil {
				browser, ok := st.Browsers[browserHash]
				if !ok || browser.Generation != expectedGeneration || !now().Before(browser.Expires) {
					return problem(403, "AUTH_STATE_INVALID", "Login state changed")
				}
				if challengeHash != "" {
					challenge, ok := st.EmailLogins[challengeHash]
					if !ok || challenge.Generation != expectedGeneration || challenge.Consumed || challenge.Attempts >= 5 || !now().Before(challenge.Expires) {
						return problem(400, "EMAIL_CODE_INVALID", "Invalid or expired verification code")
					}
				}
			}
			var user User
			for _, u := range st.Users {
				if u.Source == p.config.Source && u.Subject == id.Subject {
					user = u
					break
				}
			}
			if user.ID == "" {
				invite := p.config.Source + "\x00" + id.Subject
				if p.config.Registration != "jit" && !at.Before(st.Invitations[invite]) {
					return problem(403, "REGISTRATION_DENIED", "Invitation required")
				}
				delete(st.Invitations, invite)
				user = User{ID: randomID("usr"), Source: p.config.Source, Subject: id.Subject, Status: "active", AuthVersion: 1}
			}
			if user.Status != "active" {
				return problem(403, "USER_DISABLED", "User disabled")
			}
			user.Name = id.Name
			user.Email = id.Email
			st.Users[user.ID] = user
			st.Revision++
			session.User = user.ID
			session.AuthVersion = user.AuthVersion
			oldSessionHash := ""
			if old := s.cookie(r, "session"); old != "" {
				oldSessionHash = digest(old)
			}
			if s.Store.pool == nil {
				if challengeHash != "" {
					challenge := st.EmailLogins[challengeHash]
					challenge.Attempts++
					challenge.Consumed = true
					st.EmailLogins[challengeHash] = challenge
				}
				if oldSessionHash != "" {
					delete(st.Sessions, oldSessionHash)
				}
				st.Sessions[session.Hash] = session
				browser := st.Browsers[browserHash]
				browser.Generation++
				st.Browsers[browserHash] = browser
			} else {
				st.pendingLogin = &pendingLoginCommit{Session: session, DeleteSessionHash: oldSessionHash, BrowserHash: browserHash, ExpectedGeneration: expectedGeneration, EmailChallengeHash: challengeHash}
			}
			s.audit(st, Principal{User: user, Kind: "session"}, r, "auth.login", user.ID, true)
			return nil
		})
		if err == nil {
			break
		}
		var conflict *APIError
		if attempt == 0 && errors.As(err, &conflict) && conflict.Code == "REVISION_CONFLICT" {
			// A concurrent callback may have created the same stable identity.
			// Reload once so both logins converge on that durable user row.
			continue
		}
		return err
	}
	return nil
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
	device, user := "", ""
	for attempt := 0; attempt < 8; attempt++ {
		device = randomToken()
		// Fourteen hexadecimal characters carry 56 bits. The previous eight
		// characters were only 32 bits and could be enumerated by an authenticated
		// attacker despite the short lifetime.
		rawUserCode := strings.ToUpper(digest(randomToken())[:14])
		user = rawUserCode[:7] + "-" + rawUserCode[7:]
		e := s.Store.insertDevice(r.Context(), Device{CodeHash: digest(device), UserCode: user, Expires: now().Add(5 * time.Minute)})
		if e == nil {
			break
		}
		if !errors.Is(e, errDeviceCodeCollision) {
			s.writeError(w, r, e)
			return
		}
		device = ""
	}
	if device == "" {
		s.writeError(w, r, errors.New("unable to allocate a unique device code"))
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
	hash := digest(input.Code)
	device, ok, e := s.Store.deviceByCode(r.Context(), hash)
	if e != nil {
		s.writeError(w, r, e)
		return
	}
	if !ok || !now().Before(device.Expires) || device.Consumed {
		s.writeError(w, r, problem(400, "DEVICE_EXPIRED", "Login expired or consumed"))
		return
	}
	if !device.Approved {
		s.writeError(w, r, problem(409, "AUTHORIZATION_PENDING", "Waiting for browser approval"))
		return
	}
	token, collected, e := s.Store.consumeDevice(r.Context(), hash)
	if e != nil {
		s.writeError(w, r, e)
		return
	}
	if !collected {
		s.writeError(w, r, problem(400, "DEVICE_EXPIRED", "Login expired or consumed"))
		return
	}
	s.json(w, 200, map[string]any{"token": token, "expires_in": 3600, "token_type": "Bearer", "audience": "krun-control-api"})
}
func (s *Server) deviceApprove(st *State, p Principal, r *http.Request) (any, int, error) {
	if p.Kind != "session" {
		return nil, 0, forbidden()
	}
	for _, limit := range []struct {
		scope string
		key   string
		max   int
	}{
		{scope: "cli-approve-user", key: p.User.ID, max: 10},
		{scope: "cli-approve-ip", key: s.clientIP(r), max: 30},
	} {
		exceeded, err := s.Store.RateLimitExceeded(r.Context(), limit.scope, limit.key, time.Minute, limit.max)
		if err != nil {
			return nil, 0, err
		}
		if exceeded {
			return nil, 0, problem(429, "RATE_LIMITED", "Try again later")
		}
	}
	var input struct {
		Code string `json:"user_code"`
	}
	if e := body(r, &input); e != nil {
		return nil, 0, e
	}
	device, ok, e := s.Store.deviceByUserCode(r.Context(), input.Code)
	if e != nil {
		return nil, 0, e
	}
	if !ok {
		return nil, 0, notFound()
	}
	if device.Approved || device.Consumed || !now().Before(device.Expires) {
		return nil, 0, problem(400, "DEVICE_EXPIRED", "Approval expired")
	}
	token := "krun_cli_" + randomToken()
	at := now()
	session := Session{User: p.User.ID, Hash: digest(token), AuthVersion: p.User.AuthVersion, Created: at, Seen: at, Absolute: at.Add(time.Hour), CLI: true}
	s.audit(st, p, r, "auth.cli.approve", p.User.ID, true)
	var approved bool
	if s.Store.pool != nil {
		audits := append([]Audit(nil), st.pendingAudits...)
		st.pendingAudits = nil
		approved, e = s.Store.approveDeviceAudited(r.Context(), device.CodeHash, token, session, audits)
	} else {
		approved, e = s.Store.approveDevice(r.Context(), device.CodeHash, token, session)
	}
	if e != nil {
		return nil, 0, e
	}
	if !approved {
		return nil, 0, problem(400, "DEVICE_EXPIRED", "Approval expired")
	}
	return map[string]string{"status": "approved"}, 200, nil
}
