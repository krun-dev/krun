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
	if s.limited(r) {
		s.writeError(w, r, problem(429, "RATE_LIMITED", "Try again later"))
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
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	err = s.Store.Update(r.Context(), func(st *State) error {
		b, ok := st.Browsers[browserHash]
		if !ok || !now().Before(b.Expires) || !s.validCSRF(r, b.CSRF) {
			return problem(403, "CSRF_INVALID", "Login CSRF verification failed")
		}
		s.cleanup(st)
		sentFromIP := 0
		for _, v := range st.EmailLogins {
			if v.IP == ip && now().Sub(v.Sent) < time.Minute {
				sentFromIP++
			}
			if (v.Email == email || v.Browser == browserHash) && now().Sub(v.Sent) < time.Minute {
				return problem(429, "RATE_LIMITED", "Wait 60 seconds before requesting another code")
			}
		}
		if sentFromIP >= 10 {
			return problem(429, "RATE_LIMITED", "Try again later")
		}
		b.Generation++
		b.Expires = now().Add(15 * time.Minute)
		st.Browsers[browserHash] = b
		st.EmailLogins[digest(id)] = EmailLogin{Provider: p.config.ID, Browser: browserHash, Fingerprint: loginFingerprint(p.config), Email: email, CodeHash: digest(id + "\x00" + code), ReturnTo: returnTo, IP: ip, Generation: b.Generation, Sent: now(), Expires: now().Add(10 * time.Minute)}
		return nil
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.sendEmail(r.Context(), email, code, localize.RequestLanguage(r.Header.Get("Accept-Language"))); err != nil {
		// Retain the cooldown while making an undelivered code unusable.
		cleanupContext, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
		defer cancel()
		_ = s.Store.Update(cleanupContext, func(st *State) error {
			v := st.EmailLogins[digest(id)]
			v.Consumed = true
			st.EmailLogins[digest(id)] = v
			return nil
		})
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
	if s.limited(r) {
		s.writeError(w, r, problem(429, "RATE_LIMITED", "Try again later"))
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
	var failure error
	var returnTo string
	err := s.Store.Update(r.Context(), func(st *State) error {
		browserHash := digest(s.cookie(r, "browser"))
		b, ok := st.Browsers[browserHash]
		if !ok || !now().Before(b.Expires) || !s.validCSRF(r, b.CSRF) {
			return problem(403, "CSRF_INVALID", "Login CSRF verification failed")
		}
		key := digest(input.Challenge)
		v, ok := st.EmailLogins[key]
		if !ok || v.Provider != p.config.ID || v.Browser != browserHash || v.Fingerprint != loginFingerprint(p.config) || v.Generation != b.Generation || v.Consumed || v.Attempts >= 5 || !now().Before(v.Expires) {
			return problem(400, "EMAIL_CODE_INVALID", "Invalid or expired verification code")
		}
		v.Attempts++
		if len(input.Code) != 6 || subtle.ConstantTimeCompare([]byte(digest(input.Challenge+"\x00"+input.Code)), []byte(v.CodeHash)) != 1 {
			st.EmailLogins[key] = v
			s.audit(st, Principal{Kind: "anonymous"}, r, "auth.login.failed.EMAIL_CODE_INVALID", p.config.ID, false)
			failure = problem(400, "EMAIL_CODE_INVALID", "Invalid or expired verification code")
			return nil // Commit the failed attempt before returning its public error.
		}
		v.Consumed = true
		st.EmailLogins[key] = v
		failure = s.finishLogin(st, r, p, browserHash, identity{Subject: v.Email, Name: v.Email, Email: v.Email}, sessionToken)
		if failure != nil {
			s.audit(st, Principal{Kind: "anonymous"}, r, "auth.login.failed."+failure.Error(), p.config.ID, false)
		}
		returnTo = v.ReturnTo
		return nil
	})
	if err == nil {
		err = failure
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.setCookie(w, "session", sessionToken, 8*3600)
	s.json(w, 200, map[string]string{"redirect_url": returnTo})
}
