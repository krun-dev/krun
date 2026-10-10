package control

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func emailTestConfig() Config {
	return Config{Development: true, PublicURL: "http://127.0.0.1:8080", DB: "postgres://test-only/krun_test", SMTP: SMTPConfig{Host: "127.0.0.1", Port: 2525, From: "krun@example.com", TLS: "none"}, Providers: []ProviderConfig{{ID: "email", Source: "email-test", Type: "email", Name: "Email"}}}
}

func emailTestServer(t *testing.T, store *Store) (*Server, *string) {
	t.Helper()
	s, err := NewServer(context.Background(), emailTestConfig(), store)
	if err != nil {
		t.Fatal(err)
	}
	code := new(string)
	s.sendEmail = func(_ context.Context, to, value, language string) error {
		if to != "person@example.com" || language != "en" {
			t.Errorf("unexpected delivery: %s %s", to, language)
		}
		*code = value
		return nil
	}
	return s, code
}

func emailBrowser(t *testing.T, s *Server) ([]*http.Cookie, string) {
	t.Helper()
	w := browserRequest(s, "GET", "/auth/csrf", nil, nil)
	var data struct {
		Token string `json:"csrf_token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	return w.Result().Cookies(), data.Token
}

func emailRequest(s *Server, path string, value any, cookies []*http.Cookie, csrf string) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(value)
	r := httptest.NewRequest("POST", path, strings.NewReader(string(raw)))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept-Language", "en")
	r.Header.Set("Origin", s.Config.PublicURL)
	r.Header.Set("X-CSRF-Token", csrf)
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func emailChallenge(t *testing.T, s *Server, cookies []*http.Cookie, csrf string) string {
	t.Helper()
	w := emailRequest(s, "/auth/email/email/code", map[string]string{"email": " Person@Example.COM ", "return_to": "/console/?cli=ABCDEFG"}, cookies, csrf)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var data struct {
		ID string `json:"challenge_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil || data.ID == "" {
		t.Fatal(err)
	}
	if strings.Contains(w.Body.String(), "code_hash") {
		t.Fatal("code leaked")
	}
	return data.ID
}

func TestEmailLoginIdentitySessionAndReplay(t *testing.T) {
	store := memoryStore()
	s, code := emailTestServer(t, store)
	cookies, csrf := emailBrowser(t, s)
	challenge := emailChallenge(t, s, cookies, csrf)
	if len(*code) != 6 {
		t.Fatal("invalid code length")
	}
	input := map[string]string{"challenge_id": challenge, "code": *code}
	w := emailRequest(s, "/auth/email/email/verify", input, cookies, csrf)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "/console/?cli=ABCDEFG") {
		t.Fatal(w.Code, w.Body.String())
	}
	cookies = append(cookies, w.Result().Cookies()...)
	me := browserRequest(s, "GET", "/auth/me", nil, cookies)
	var data struct {
		User  User
		Admin bool `json:"system_admin"`
	}
	if err := json.Unmarshal(me.Body.Bytes(), &data); err != nil || me.Code != 200 || data.User.Email != "person@example.com" || data.User.Subject != data.User.Email || data.Admin {
		t.Fatal(me.Code, me.Body.String(), err)
	}
	if err := store.Update(context.Background(), func(st *State) error {
		if len(st.Assignments) != 0 || len(st.Members) != 0 {
			t.Error("email login granted privileges")
		}
		if st.EmailLogins[digest(challenge)].CodeHash == *code {
			t.Error("plaintext code stored")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if w := emailRequest(s, "/auth/email/email/verify", input, cookies, csrf); w.Code != 400 {
		t.Fatal("replayed code accepted", w.Code)
	}
	// A fresh browser can log in to the same local identity without merging SSO identities.
	_ = store.Update(context.Background(), func(st *State) error {
		for k, v := range st.EmailLogins {
			v.Sent = now().Add(-time.Minute)
			st.EmailLogins[k] = v
		}
		st.Users["sso"] = User{ID: "sso", Source: "other", Subject: "person@example.com", Email: "person@example.com", Status: "active"}
		return nil
	})
	cookies, csrf = emailBrowser(t, s)
	challenge = emailChallenge(t, s, cookies, csrf)
	w = emailRequest(s, "/auth/email/email/verify", map[string]string{"challenge_id": challenge, "code": *code}, cookies, csrf)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	me = browserRequest(s, "GET", "/auth/me", nil, append(cookies, w.Result().Cookies()...))
	var second struct{ User User }
	_ = json.Unmarshal(me.Body.Bytes(), &second)
	if second.User.ID != data.User.ID {
		t.Fatal("email identity changed or merged with SSO")
	}
}

func TestEmailCodeSecurityBoundaries(t *testing.T) {
	for _, test := range []string{"csrf", "origin", "other-browser", "expired", "attempt-limit", "superseded", "config-change", "delivery-failure", "cooldown", "redirect", "invalid-email", "user-disabled", "invite-only"} {
		t.Run(test, func(t *testing.T) {
			store := memoryStore()
			s, code := emailTestServer(t, store)
			cookies, csrf := emailBrowser(t, s)
			if test == "redirect" || test == "invalid-email" {
				input := map[string]string{"email": "person@example.com", "return_to": "https://evil.example.com"}
				if test == "invalid-email" {
					input["email"] = "Person <person@example.com>"
					input["return_to"] = "/console/"
				}
				w := emailRequest(s, "/auth/email/email/code", input, cookies, csrf)
				if w.Code != 400 {
					t.Fatal(w.Code, w.Body.String())
				}
				return
			}
			if test == "delivery-failure" {
				s.sendEmail = func(context.Context, string, string, string) error { return errors.New("smtp credential SECRET") }
				w := emailRequest(s, "/auth/email/email/code", map[string]string{"email": "person@example.com"}, cookies, csrf)
				if w.Code != 503 || strings.Contains(w.Body.String(), "SECRET") {
					t.Fatal(w.Code, w.Body.String())
				}
				_ = store.Update(context.Background(), func(st *State) error {
					for _, v := range st.EmailLogins {
						if !v.Consumed {
							t.Error("undelivered code active")
						}
					}
					return nil
				})
				return
			}
			if test == "invite-only" {
				s.providers["email"].config.Registration = "invite_only"
			}
			challenge := emailChallenge(t, s, cookies, csrf)
			input := map[string]string{"challenge_id": challenge, "code": *code}
			want := 400
			switch test {
			case "csrf":
				csrf = "bad"
				want = 403
			case "origin":
				want = 403
			case "other-browser":
				cookies, csrf = emailBrowser(t, s)
			case "expired":
				_ = store.Update(context.Background(), func(st *State) error {
					v := st.EmailLogins[digest(challenge)]
					v.Expires = now().Add(-time.Second)
					st.EmailLogins[digest(challenge)] = v
					return nil
				})
			case "attempt-limit":
				wrong := map[string]string{"challenge_id": challenge, "code": "wrong"}
				for i := 0; i < 5; i++ {
					if w := emailRequest(s, "/auth/email/email/verify", wrong, cookies, csrf); w.Code != 400 {
						t.Fatal(w.Code)
					}
				}
			case "superseded":
				_ = store.Update(context.Background(), func(st *State) error {
					b := st.Browsers[digest(cookies[0].Value)]
					b.Generation++
					st.Browsers[digest(cookies[0].Value)] = b
					return nil
				})
			case "config-change":
				s.providers["email"].config.Name = "changed"
			case "cooldown":
				otherCookies, otherCSRF := emailBrowser(t, s)
				w := emailRequest(s, "/auth/email/email/code", map[string]string{"email": "person@example.com"}, otherCookies, otherCSRF)
				if w.Code != 429 {
					t.Fatal(w.Code, w.Body.String())
				}
				return
			case "user-disabled":
				want = 403
				_ = store.Update(context.Background(), func(st *State) error {
					st.Users["disabled"] = User{ID: "disabled", Source: "email-test", Subject: "person@example.com", Status: "disabled"}
					return nil
				})
			case "invite-only":
				want = 403
			}
			var w *httptest.ResponseRecorder
			if test == "origin" {
				s.Config.PublicURL = "http://127.0.0.1:8080"
				raw, _ := json.Marshal(input)
				r := httptest.NewRequest("POST", "/auth/email/email/verify", strings.NewReader(string(raw)))
				r.Header.Set("Content-Type", "application/json")
				r.Header.Set("Origin", "https://evil.example.com")
				r.Header.Set("X-CSRF-Token", csrf)
				for _, c := range cookies {
					r.AddCookie(c)
				}
				w = httptest.NewRecorder()
				s.ServeHTTP(w, r)
			} else {
				w = emailRequest(s, "/auth/email/email/verify", input, cookies, csrf)
			}
			if w.Code != want {
				t.Fatal(w.Code, w.Body.String())
			}
			if len(w.Result().Cookies()) != 0 {
				t.Fatal("failure issued session")
			}
		})
	}
}

func TestDisabledLoginSources(t *testing.T) {
	s, store := testServer(t)
	falseValue := false
	c := s.Config
	c.Providers[0].Enabled = &falseValue
	disabled, err := NewServer(context.Background(), c, store)
	if err != nil {
		t.Fatal(err)
	}
	if w := browserRequest(disabled, "GET", "/auth/providers", nil, nil); strings.Contains(w.Body.String(), `"id":"local"`) {
		t.Fatal("disabled source advertised")
	}
	if w := browserRequest(disabled, "POST", "/auth/local/login", url.Values{}, nil); w.Code != 404 {
		t.Fatal(w.Code)
	}
	if w := request(disabled, "GET", "/api/v1/me", "krun_cli_admin", ""); w.Code != 401 {
		t.Fatal("disabled source credential accepted", w.Code)
	}
	c = emailTestConfig()
	c.Providers[0].Enabled = &falseValue
	c.SMTP = SMTPConfig{}
	emailDisabled, err := NewServer(context.Background(), c, memoryStore())
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/auth/email/email/code", "/auth/email/email/verify", "/auth/email/login"} {
		if w := emailRequest(emailDisabled, path, map[string]string{}, nil, ""); w.Code != 404 {
			t.Fatal(path, w.Code)
		}
	}
	// A disabled dev source can remain in a production config without exposing it.
	c.Development = false
	c.PublicURL = "https://krun.example.com"
	c.Providers[0].Type = "dev"
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestEmailSMTPConfig(t *testing.T) {
	for _, test := range []string{"missing", "from-injection", "port", "tls", "production-plaintext", "auth-incomplete"} {
		t.Run(test, func(t *testing.T) {
			c := emailTestConfig()
			switch test {
			case "missing":
				c.SMTP.Host = ""
			case "from-injection":
				c.SMTP.From = "mail@example.com\r\nBcc:other@example.com"
			case "port":
				c.SMTP.Port = 70000
			case "tls":
				c.SMTP.TLS = "bad"
			case "production-plaintext":
				c.Development = false
				c.PublicURL = "https://krun.example.com"
			case "auth-incomplete":
				c.SMTP.Password = "SECRET"
			}
			if err := c.Validate(); err == nil || strings.Contains(err.Error(), "SECRET") {
				t.Fatal(err)
			}
		})
	}
	c := emailTestConfig()
	c.SMTP.TLS = ""
	c.SMTP.Port = 0
	if err := c.Validate(); err != nil || c.SMTP.TLS != "starttls" || c.SMTP.Port != 587 {
		t.Fatal(c.SMTP, err)
	}
}

func TestVerificationMailSMTP(t *testing.T) {
	for _, language := range []string{"zh-CN", "en"} {
		t.Run(language, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			message := make(chan string, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				fmt.Fprint(conn, "220 test SMTP\r\n")
				reader := bufio.NewReader(conn)
				var received strings.Builder
				for {
					line, err := reader.ReadString('\n')
					if err != nil {
						return
					}
					switch {
					case strings.HasPrefix(line, "EHLO"):
						fmt.Fprint(conn, "250-test\r\n250 OK\r\n")
					case strings.HasPrefix(line, "MAIL FROM:"), strings.HasPrefix(line, "RCPT TO:"):
						fmt.Fprint(conn, "250 OK\r\n")
					case strings.HasPrefix(line, "DATA"):
						fmt.Fprint(conn, "354 send\r\n")
						for {
							line, err = reader.ReadString('\n')
							if err != nil {
								return
							}
							if line == ".\r\n" {
								break
							}
							received.WriteString(line)
						}
						message <- received.String()
						fmt.Fprint(conn, "250 queued\r\n")
					case strings.HasPrefix(line, "QUIT"):
						fmt.Fprint(conn, "221 bye\r\n")
						return
					default:
						fmt.Fprint(conn, "500 unsupported\r\n")
					}
				}
			}()
			_, port, _ := net.SplitHostPort(listener.Addr().String())
			c := emailTestConfig()
			c.SMTP.Port, _ = strconv.Atoi(port)
			if err := c.sendVerificationEmail(context.Background(), "person@example.com", "123456", language); err != nil {
				t.Fatal(err)
			}
			select {
			case raw := <-message:
				want := "Your sign-in code"
				if language == "zh-CN" {
					want = "您的登录验证码"
				}
				if !strings.Contains(raw, want) || !strings.Contains(raw, "123456") || !strings.Contains(raw, "To: person@example.com\r\n") {
					t.Fatal(raw)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("no SMTP message")
			}
		})
	}
}

func TestPostgresEmailLoginRestartAndConcurrentConsumption(t *testing.T) {
	store := postgresTestStore(t)
	if err := store.Update(context.Background(), func(st *State) error { *st = *newState(); return nil }); err != nil {
		t.Fatal(err)
	}
	s, code := emailTestServer(t, store)
	cookies, csrf := emailBrowser(t, s)
	challenge := emailChallenge(t, s, cookies, csrf)
	restarted, err := NewServer(context.Background(), s.Config, store)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan int, 2)
	for _, replica := range []*Server{s, restarted} {
		wg.Add(1)
		go func(server *Server) {
			defer wg.Done()
			w := emailRequest(server, "/auth/email/email/verify", map[string]string{"challenge_id": challenge, "code": *code}, cookies, csrf)
			results <- w.Code
		}(replica)
	}
	wg.Wait()
	close(results)
	success := 0
	for status := range results {
		if status == 200 {
			success++
		} else if status != 400 {
			t.Fatal(status)
		}
	}
	if success != 1 {
		t.Fatal("code was not consumed exactly once", success)
	}
}
