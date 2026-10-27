package control

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAPIOnlyDoesNotServeFilesOrRedirectConsole(t *testing.T) {
	s, _ := testServer(t)
	if s.Config.StaticDir != "" {
		t.Fatal("omitted static_dir should keep the backend API-only")
	}
	for _, path := range []string{"/", "/console/", "/console/assets/app.js", "/config/example.yaml", "/go.mod"} {
		w := request(s, http.MethodGet, path, "", "")
		if w.Code != http.StatusNotFound || w.Header().Get("Location") != "" || !strings.Contains(w.Body.String(), "RESOURCE_NOT_FOUND") {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
	}
	for _, path := range []string{"/api/auth/providers", "/api/healthz"} {
		if w := request(s, http.MethodGet, path, "", ""); w.Code != http.StatusOK {
			t.Fatalf("API-only endpoint %s: %d", path, w.Code)
		}
	}
}

func TestSeparatedFrontendProxyLogin(t *testing.T) {
	s, store := testServer(t)
	backend := httptest.NewServer(s)
	defer backend.Close()
	backendURL, _ := url.Parse(backend.URL)
	proxy := httputil.NewSingleHostReverseProxy(backendURL)
	frontendMux := http.NewServeMux()
	frontendMux.Handle("/api/", proxy)
	frontendMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "independent frontend")
	})
	frontend := httptest.NewServer(frontendMux)
	defer frontend.Close()
	s.Config.PublicURL = frontend.URL
	s.routes()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 5 * time.Second}
	response, err := client.Get(frontend.URL + "/api/auth/csrf")
	if err != nil {
		t.Fatal(err)
	}
	var csrf struct {
		Token string `json:"csrf_token"`
	}
	err = json.NewDecoder(response.Body).Decode(&csrf)
	response.Body.Close()
	if err != nil || csrf.Token == "" {
		t.Fatal(err)
	}
	loginRequest, _ := http.NewRequest(http.MethodPost, frontend.URL+"/api/auth/local/login", strings.NewReader(url.Values{"return_to": {"/?page=workers"}}.Encode()))
	loginRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loginRequest.Header.Set("Origin", frontend.URL)
	loginRequest.Header.Set("X-CSRF-Token", csrf.Token)
	// Follow the callback through the frontend proxy and then the local return path.
	response, err = client.Do(loginRequest)
	if err != nil {
		t.Fatal(err)
	}
	content, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || string(content) != "independent frontend" || response.Request.URL.String() != frontend.URL+"/?page=workers" {
		t.Fatal("login did not return to the independent frontend", response.StatusCode, response.Request.URL, string(content))
	}
	response, err = client.Get(frontend.URL + "/api/auth/me")
	if err != nil {
		t.Fatal(err)
	}
	var me struct{ User User }
	err = json.NewDecoder(response.Body).Decode(&me)
	response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || me.User.Subject != "developer" {
		t.Fatal(response.StatusCode, me, err)
	}
	if err := store.Update(context.Background(), PartitionAll, func(st *State) error {
		if st.Users[me.User.ID].Subject != "developer" {
			t.Error("proxy login was not persisted")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Fetch session CSRF via the same origin and log out through the proxy.
	response, err = client.Get(frontend.URL + "/api/auth/csrf")
	if err != nil {
		t.Fatal(err)
	}
	err = json.NewDecoder(response.Body).Decode(&csrf)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	logoutRequest, _ := http.NewRequest(http.MethodPost, frontend.URL+"/api/auth/logout", nil)
	logoutRequest.Header.Set("Origin", frontend.URL)
	logoutRequest.Header.Set("X-CSRF-Token", csrf.Token)
	response, err = client.Do(logoutRequest)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatal("proxy logout failed", response.StatusCode)
	}
	response, err = client.Get(frontend.URL + "/api/v1/me")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatal("logged-out session remained active", response.StatusCode)
	}
}

func TestDevelopmentConsoleUsesPublicOrigin(t *testing.T) {
	s, _ := testServer(t)
	s.Config.PublicURL = "http://127.0.0.1:5173"
	s.Config.StaticDir = t.TempDir()
	if err := os.WriteFile(filepath.Join(s.Config.StaticDir, "index.html"), []byte("console"), 0600); err != nil {
		t.Fatal(err)
	}
	s.routes()
	for _, tc := range []struct{ path, target string }{
		{"/", "/"},
		{"/?cli=ABCD", "/?cli=ABCD"},
		{"/assets/app.js", "/assets/app.js"},
	} {
		w := request(s, http.MethodGet, "http://127.0.0.1:8080"+tc.path, "", "")
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != s.Config.PublicURL+tc.target {
			t.Fatalf("%s: status=%d location=%q", tc.path, w.Code, w.Header().Get("Location"))
		}
	}
	if w := request(s, http.MethodGet, s.Config.PublicURL+"/", "", ""); w.Code != http.StatusOK || w.Body.String() != "console" {
		t.Fatalf("canonical console did not render: %d %s", w.Code, w.Body.String())
	}
	for _, path := range []string{"/api/auth/providers", "/api/healthz"} {
		if w := request(s, http.MethodGet, "http://127.0.0.1:8080"+path, "", ""); w.Code != http.StatusOK {
			t.Fatalf("backend endpoint %s unexpectedly redirected: %d", path, w.Code)
		}
	}
	// Redirecting the console must not relax origin checks on login writes.
	csrf := browserRequest(s, http.MethodGet, "/api/auth/csrf", nil, nil)
	var token struct {
		Value string `json:"csrf_token"`
	}
	if err := json.Unmarshal(csrf.Body.Bytes(), &token); err != nil {
		t.Fatal(err)
	}
	form := url.Values{"csrf_token": {token.Value}}
	r := httptest.NewRequest(http.MethodPost, "/api/auth/local/login", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", "http://127.0.0.1:8080")
	for _, cookie := range csrf.Result().Cookies() {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "CSRF_INVALID") {
		t.Fatalf("wrong-origin login was not rejected: %d %s", w.Code, w.Body.String())
	}
	r.Header.Set("Origin", s.Config.PublicURL)
	r.Body = http.NoBody
	r.Form = form
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("canonical-origin login failed: %d %s", w.Code, w.Body.String())
	}
	// Production proxy routing keeps its existing behavior.
	s.Config.Development = false
	s.routes()
	if w := request(s, http.MethodGet, "http://127.0.0.1:8080/", "", ""); w.Code != http.StatusOK {
		t.Fatalf("production console routing changed: %d", w.Code)
	}
}
