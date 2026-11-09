package control

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	kruncli "github.com/krun-dev/krun/internal/cli"
	appconfig "github.com/krun-dev/krun/internal/config"
)

func TestCLIRepositoryAuthorizeOpensProviderAndBindsCallback(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/token" || r.Method != http.MethodPost {
			t.Errorf("unexpected provider request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"fixture-user-token","token_type":"bearer","expires_in":7200}`)
	}))
	defer provider.Close()

	s, store, _ := workerTestServer(t)
	s.Config.Workers.RepositoryProviders = []appconfig.WorkerRepositoryProvider{
		{ID: "github", Type: "github", Name: "GitHub", Enabled: true, ClientID: "fixture-client", GitHubAppSlug: "fixture-app"},
		{ID: "gitlab", Type: "gitlab", Name: "GitLab", Enabled: true, ClientID: "fixture-client", BaseURL: provider.URL},
	}
	s.routes()
	if err := store.Update(t.Context(), PartitionAll, func(st *State) error {
		for _, user := range []string{"admin", "viewer"} {
			session := st.Sessions[digest("krun_cli_"+user)]
			session.CLI = false
			st.Sessions[digest("browser-"+user)] = session
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(s)
	defer server.Close()
	t.Setenv("KRUN_ENDPOINT", server.URL)
	t.Setenv("KRUN_TOKEN", "krun_cli_admin")

	browse := func(method, path, user string, browser *http.Cookie) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, nil)
		if user != "" {
			req.AddCookie(&http.Cookie{Name: s.cookieName("session"), Value: "browser-" + user})
		}
		if browser != nil {
			req.AddCookie(browser)
		}
		out := httptest.NewRecorder()
		s.ServeHTTP(out, req)
		return out
	}

	for _, tc := range []struct {
		id          string
		destination string
	}{
		{"github", "https://github.com/login/oauth/authorize"},
		{"gitlab", provider.URL + "/oauth/authorize"},
	} {
		t.Run(tc.id, func(t *testing.T) {
			var diagnostics bytes.Buffer
			args := []string{"workers", "repository", "authorize", "--org", "org-a", "--provider", tc.id, "--browser=false", "--profile", t.TempDir() + "/cli.yaml"}
			if err := kruncli.Run(t.Context(), args, strings.NewReader(""), io.Discard, &diagnostics); err != nil {
				t.Fatal(err)
			}
			fields := strings.Fields(diagnostics.String())
			if len(fields) < 2 || fields[0] != "Open" {
				t.Fatalf("missing authorization link: %s", &diagnostics)
			}
			launch, err := url.Parse(fields[1])
			if err != nil || launch.Host != strings.TrimPrefix(server.URL, "http://") || launch.Query().Get("ticket") == "" {
				t.Fatalf("invalid authorization link: %v", err)
			}
			if got := browse(http.MethodGet, launch.RequestURI(), "", nil); got.Code != 401 {
				t.Fatalf("anonymous browser launch returned %d", got.Code)
			}
			if got := browse(http.MethodGet, launch.RequestURI(), "viewer", nil); got.Code != 403 {
				t.Fatalf("wrong-account browser launch returned %d", got.Code)
			}
			if got := browse(http.MethodHead, launch.RequestURI(), "admin", nil); got.Code < 400 {
				t.Fatalf("HEAD launched authorization: %d", got.Code)
			}
			start := browse(http.MethodGet, launch.RequestURI(), "admin", nil)
			if start.Code != 303 {
				t.Fatalf("browser launch returned %d: %s", start.Code, start.Body.String())
			}
			location, err := url.Parse(start.Header().Get("Location"))
			if err != nil || strings.SplitN(location.String(), "?", 2)[0] != tc.destination || location.Query().Get("state") == "" {
				t.Fatalf("unexpected provider redirect: %v", err)
			}
			if got := browse(http.MethodGet, launch.RequestURI(), "admin", nil); got.Code != 403 {
				t.Fatalf("launch ticket replay returned %d", got.Code)
			}

			if tc.id == "gitlab" {
				var binding *http.Cookie
				for _, cookie := range start.Result().Cookies() {
					if cookie.Name == s.cookieName("browser") {
						binding = cookie
					}
				}
				if binding == nil {
					t.Fatal("browser binding cookie missing")
				}
				callback := "/api/v1/workers/repository/gitlab/callback?" + url.Values{
					"code": {"fixture-code"}, "state": {location.Query().Get("state")},
				}.Encode()
				if got := browse(http.MethodGet, callback, "admin", nil); !strings.Contains(got.Header().Get("Location"), "WORKER_REPOSITORY_STATE_INVALID") {
					t.Fatalf("callback without browser binding returned %d", got.Code)
				}
				if got := browse(http.MethodGet, callback, "admin", binding); !strings.Contains(got.Header().Get("Location"), "repository_status=connected") {
					t.Fatalf("OAuth callback returned %d: %s", got.Code, got.Header().Get("Location"))
				}
				connection, ok, err := store.RepositoryConnection(t.Context(), "admin", "gitlab")
				if err != nil || !ok || connection.AccessToken != "fixture-user-token" {
					t.Fatalf("repository connection was not saved: %v", err)
				}
			}
		})
	}
}
