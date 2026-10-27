package control

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"errors"
	"fmt"
	"math/big"
	"mime"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"github.com/krun-dev/krun/internal/localize"
)

func normalizedEmail(value string) (string, error) {
	value = strings.TrimSpace(value)
	address, err := mail.ParseAddress(value)
	if err != nil || address.Address != value || len(value) > 254 || strings.ContainsAny(value, "\r\n") || !strings.Contains(value, "@") {
		return "", errors.New("a valid email address is required")
	}
	// Email login treats mailbox names as case-insensitive; this is the stable subject.
	return strings.ToLower(value), nil
}

func (c *Config) validateSMTP() error {
	s := &c.SMTP
	if s.Host == "" || strings.ContainsAny(s.Host, "/\r\n \t") {
		return errors.New("SMTP host and from address required for email login")
	}
	from, err := normalizedEmail(s.From)
	if err != nil {
		return errors.New("SMTP host and from address required for email login")
	}
	s.From = from
	if s.TLS == "" {
		s.TLS = "starttls"
	}
	if !contains([]string{"starttls", "tls", "none"}, s.TLS) {
		return errors.New("SMTP tls must be starttls, tls or none")
	}
	if s.TLS == "none" && (!c.Development || (s.Host != "localhost" && !net.ParseIP(s.Host).IsLoopback())) {
		return errors.New("unencrypted SMTP is only allowed on loopback in development")
	}
	if s.Port == 0 {
		s.Port = 587
		if s.TLS == "tls" {
			s.Port = 465
		}
	}
	if s.Port < 1 || s.Port > 65535 {
		return errors.New("invalid SMTP port")
	}
	if (s.Username == "") != (s.Password == "") {
		return errors.New("SMTP username and password must be configured together")
	}
	return nil
}

func (c Config) sendVerificationEmail(ctx context.Context, to, code, language string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	address := net.JoinHostPort(c.SMTP.Host, strconv.Itoa(c.SMTP.Port))
	connection, err := (&net.Dialer{}).DialContext(ctx, "tcp", address)
	if err != nil {
		return err
	}
	defer connection.Close()
	deadline, _ := ctx.Deadline()
	if err := connection.SetDeadline(deadline); err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { _ = connection.Close() })
	defer stop()
	tlsConfig := &tls.Config{ServerName: c.SMTP.Host, MinVersion: tls.VersionTLS12}
	var smtpConnection net.Conn = connection
	if c.SMTP.TLS == "tls" {
		tlsConnection := tls.Client(connection, tlsConfig)
		if err := tlsConnection.HandshakeContext(ctx); err != nil {
			return err
		}
		smtpConnection = tlsConnection
	}
	client, err := smtp.NewClient(smtpConnection, c.SMTP.Host)
	if err != nil {
		return err
	}
	defer client.Close()
	if c.SMTP.TLS == "starttls" {
		// Require TLS rather than silently falling back to plaintext.
		if err := client.StartTLS(tlsConfig); err != nil {
			return err
		}
	}
	if c.SMTP.Username != "" {
		if err := client.Auth(smtp.PlainAuth("", c.SMTP.Username, c.SMTP.Password, c.SMTP.Host)); err != nil {
			return err
		}
	}
	if err := client.Mail(c.SMTP.From); err != nil {
		return err
	}
	if err := client.Rcpt(to); err != nil {
		return err
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	subject := mime.QEncoding.Encode("utf-8", localize.Text(language, "Your krun sign-in code"))
	message := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n%s\r\n", c.SMTP.From, to, subject, now().Format(time.RFC1123Z), localize.Format(language, "Your sign-in code is %s. It expires in 10 minutes. If you did not request this code, ignore this email.", code))
	if _, err := writer.Write([]byte(message)); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	// DATA has already been accepted; a failed QUIT must not invalidate delivery.
	_ = client.Quit()
	return nil
}

func emailUnavailable() error {
	return problem(404, "AUTH_PROVIDER_UNAVAILABLE", "Provider unavailable")
}

func (s *Server) emailCodeHandler(w http.ResponseWriter, r *http.Request) {
	p, ok := s.providers[r.PathValue("provider")]
	if !ok || p.config.Type != "email" {
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
	email, err := normalizedEmail(input.Email)
	if err != nil {
		s.writeError(w, r, problem(400, "INVALID_REQUEST", err.Error()))
		return
	}
	returnTo, err := loginReturnPath(input.ReturnTo)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	number, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	code := fmt.Sprintf("%06d", number.Int64())
	id := randomToken()
	browserHash := digest(s.cookie(r, "browser"))
	ip := s.clientIP(r)
	browser, ok, err := s.Store.browserFor(r.Context(), browserHash)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if !ok || !now().Before(browser.Expires) || !s.validCSRF(r, browser.CSRF) {
		s.writeError(w, r, problem(403, "CSRF_INVALID", "Login CSRF verification failed"))
		return
	}
	challenge := EmailLogin{Provider: p.config.ID, Browser: browserHash, Fingerprint: loginFingerprint(p.config), Email: email, CodeHash: digest(id + "\x00" + code), ReturnTo: returnTo, IP: ip, Sent: now(), Expires: now().Add(10 * time.Minute)}
	if _, err = s.Store.beginEmailLogin(r.Context(), browserHash, browser, digest(id), challenge, time.Minute, 10); err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.sendEmail(r.Context(), email, code, localize.RequestLanguage(r.Header.Get("Accept-Language"))); err != nil {
		// Retain the cooldown while making an undelivered code unusable.
		cleanupContext, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
		defer cancel()
		_ = s.Store.invalidateEmailLogin(cleanupContext, digest(id))
		s.writeError(w, r, problem(503, "EMAIL_DELIVERY_FAILED", "Unable to send the verification email. Try again later"))
		return
	}
	s.json(w, 200, map[string]any{"challenge_id": id, "expires_in": 600, "retry_after": 60})
}

func (s *Server) emailVerifyHandler(w http.ResponseWriter, r *http.Request) {
	p, ok := s.providers[r.PathValue("provider")]
	if !ok || p.config.Type != "email" {
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
	browser, hasBrowser, err := s.Store.browserFor(r.Context(), browserHash)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if !hasBrowser || !now().Before(browser.Expires) || !s.validCSRF(r, browser.CSRF) {
		s.writeError(w, r, problem(403, "CSRF_INVALID", "Login CSRF verification failed"))
		return
	}
	key := digest(input.Challenge)
	challenge, found, err := s.Store.emailLogin(r.Context(), key)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if !found || challenge.Provider != p.config.ID || challenge.Browser != browserHash || challenge.Fingerprint != loginFingerprint(p.config) || challenge.Generation != browser.Generation || challenge.Consumed || challenge.Attempts >= 5 || !now().Before(challenge.Expires) {
		s.writeError(w, r, problem(400, "EMAIL_CODE_INVALID", "Invalid or expired verification code"))
		return
	}
	if len(input.Code) != 6 || subtle.ConstantTimeCompare([]byte(digest(input.Challenge+"\x00"+input.Code)), []byte(challenge.CodeHash)) != 1 {
		// Persist the failed attempt so the retry counter cannot be reset by
		// reconnecting. The store applies the increment conditionally so
		// concurrent failures cannot overwrite one another.
		if _, e := s.Store.failEmailLogin(r.Context(), key); e != nil {
			s.writeError(w, r, e)
			return
		}
		s.recordAnonymousAudit(r, p, "auth.login.failed.EMAIL_CODE_INVALID")
		s.writeError(w, r, problem(400, "EMAIL_CODE_INVALID", "Invalid or expired verification code"))
		return
	}
	if e := s.finishEmailLogin(r.Context(), r, p, browserHash, challenge.Generation, identity{Subject: challenge.Email, Name: challenge.Email, Email: challenge.Email}, sessionToken, key); e != nil {
		s.recordAnonymousAudit(r, p, "auth.login.failed."+e.Error())
		s.writeError(w, r, e)
		return
	}
	s.setCookie(w, "session", sessionToken, 8*3600)
	s.json(w, 200, map[string]string{"redirect_url": challenge.ReturnTo})
}

// recordAnonymousAudit commits a denial entry without a state mutation.
func (s *Server) recordAnonymousAudit(r *http.Request, p *provider, action string) {
	_ = s.Store.Update(r.Context(), PartitionIAM, func(st *State) error {
		s.audit(st, Principal{Kind: "anonymous"}, r, action, p.config.ID, false)
		return nil
	})
}
