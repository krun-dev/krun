package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/krun-dev/krun/internal/configfile"
)

func TestTokenLoginPermissionsAndLogout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/me" || r.Header.Get("Authorization") != "Bearer krun_key_test" {
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"user":{"user_id":"test"}}`))
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "nested", "profile.yaml")
	var out, errOut bytes.Buffer
	args := []string{"login", "--token-stdin", "--server", server.URL, "--config", path}
	if e := Run(context.Background(), args, strings.NewReader("krun_key_test\n"), &out, &errOut); e != nil {
		t.Fatal(e)
	}
	info, e := os.Stat(path)
	if e != nil || info.Mode().Perm() != 0600 {
		t.Fatal("profile permissions", e)
	}
	raw, _ := os.ReadFile(path)
	var profile Profile
	if e := configfile.Decode(path, raw, &profile); e != nil {
		t.Fatal(e)
	}
	if profile.Token != "krun_key_test" {
		t.Fatal("token not saved")
	}
	if e = Run(context.Background(), []string{"logout", "--config", path}, strings.NewReader(""), &out, &errOut); e != nil {
		t.Fatal(e)
	}
	raw, _ = os.ReadFile(path)
	if e := configfile.Decode(path, raw, &profile); e != nil {
		t.Fatal(e)
	}
	if profile.Token != "" {
		t.Fatal("logout did not clear local API Key")
	}
}
func TestServerSwitchDoesNotLeakSavedToken(t *testing.T) {
	var received string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "profile.yaml")
	raw, _ := configfile.Marshal(path, Profile{Server: "https://old.example.com", Token: "saved-secret"})
	_ = os.WriteFile(path, raw, 0600)
	var out bytes.Buffer
	if e := Run(context.Background(), []string{"whoami", "--server", server.URL, "--config", path}, strings.NewReader(""), &out, &out); e != nil {
		t.Fatal(e)
	}
	if received != "" {
		t.Fatal("saved token sent to different server")
	}
}
func TestRejectNonHTTPSAndAPIAbsolutePaths(t *testing.T) {
	if validateServer("http://public.example.com") == nil || validateServer("https://example.com/other") == nil {
		t.Fatal("unsafe server accepted")
	}
	c := Client{Profile: Profile{Server: "https://example.com"}}
	if c.request(context.Background(), "GET", "//evil.example.com", nil, nil, false) == nil {
		t.Fatal("absolute API path accepted")
	}
}

func TestKeyLifetimeFlags(t *testing.T) {
	dir := t.TempDir()
	policyFile := filepath.Join(dir, "policy.yaml")
	_ = os.WriteFile(policyFile, []byte(`schema_version: 1
statements:
  - effect: allow
    actions: ["*"]
    resource:
      kind: "*"
      all_in_system: true
    conditions: {}
`), 0600)
	for _, tc := range []struct {
		flag    string
		seconds int
		valid   bool
	}{{"never", 0, true}, {"0", 0, true}, {"24h", 86400, true}, {"-1h", 0, false}, {"1s", 0, false}, {"invalid", 0, false}} {
		t.Run(tc.flag, func(t *testing.T) {
			called := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				if r.Method != "POST" || r.URL.Path != "/api/v1/system/api-keys" {
					t.Error("wrong key endpoint")
				}
				var input struct {
					Expires int `json:"expires_in"`
				}
				if json.NewDecoder(r.Body).Decode(&input) != nil || input.Expires != tc.seconds {
					t.Error("wrong key lifetime")
				}
				_, _ = w.Write([]byte(`{}`))
			}))
			defer server.Close()
			config := filepath.Join(dir, "profile.yaml")
			profile, _ := configfile.Marshal(config, Profile{Server: server.URL, Token: "krun_cli_test"})
			_ = os.WriteFile(config, profile, 0600)
			var out bytes.Buffer
			e := Run(context.Background(), []string{"keys", "create", "--system", "--name", "test", "--policy", policyFile, "--expires", tc.flag, "--config", config}, strings.NewReader(""), &out, &out)
			if (e == nil) != tc.valid || called != tc.valid {
				t.Fatalf("valid=%t called=%t error=%v", tc.valid, called, e)
			}
		})
	}
}

func TestAuditPaginationFlags(t *testing.T) {
	dir := t.TempDir()
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.URL.Path != "/api/v1/orgs/org-a/audit" || r.URL.Query().Get("limit") != "100" || r.URL.Query().Get("cursor") != "opaque+/=cursor" {
			t.Error("pagination parameters changed")
		}
		_, _ = w.Write([]byte("{}"))
	}))
	defer server.Close()
	config := filepath.Join(dir, "profile.yaml")
	raw, _ := configfile.Marshal(config, Profile{Server: server.URL, Token: "krun_cli_test"})
	_ = os.WriteFile(config, raw, 0600)
	var out bytes.Buffer
	err := Run(context.Background(), []string{"audit", "--org", "org-a", "--limit", "100", "--cursor", "opaque+/=cursor", "--config", config}, strings.NewReader(""), &out, &out)
	if err != nil || !called {
		t.Fatal("audit pagination request failed", err)
	}
	called = false
	err = Run(context.Background(), []string{"audit", "--limit", "501", "--config", config}, strings.NewReader(""), &out, &out)
	if err == nil || called {
		t.Fatal("invalid page size reached server")
	}
}

func TestYAMLInputReachesJSONAPI(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "request.yml")
	if err := os.WriteFile(input, []byte("name: yaml-input\nenabled: true\nsubject: 123456789012345678901234567890\n"), 0600); err != nil {
		t.Fatal(err)
	}
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.Header.Get("Content-Type") != "application/json" {
			t.Error("wrong API encoding")
		}
		decoder := json.NewDecoder(r.Body)
		decoder.UseNumber()
		var got map[string]any
		if err := decoder.Decode(&got); err != nil {
			t.Error(err)
		}
		if got["enabled"] != true || got["subject"] != json.Number("123456789012345678901234567890") || got["name"] != "yaml-input" {
			t.Error("YAML values changed in API request")
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	config := filepath.Join(dir, "profile.yaml")
	var out bytes.Buffer
	args := []string{"api", "--method", "POST", "--path", "/api/v1/test", "--file", input, "--server", server.URL, "--config", config}
	if err := Run(context.Background(), args, strings.NewReader(""), &out, &out); err != nil || !called {
		t.Fatal("YAML request failed", err)
	}
	legacy := filepath.Join(dir, "request.json")
	if err := os.WriteFile(legacy, []byte(`{"name":"legacy"}`), 0600); err != nil {
		t.Fatal(err)
	}
	args[6] = legacy
	called = false
	if err := Run(context.Background(), args, strings.NewReader(""), &out, &out); err == nil || called {
		t.Fatal("legacy JSON request accepted")
	}
}

func TestCLIRejectsJSONProfiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profile.json")
	var out bytes.Buffer
	if err := Run(context.Background(), []string{"logout", "--config", path}, strings.NewReader(""), &out, &out); err == nil {
		t.Fatal("new JSON config accepted")
	}
	if err := os.WriteFile(path, []byte(`{"server":"http://127.0.0.1:8080","token":"old-token"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadProfile(path); err == nil {
		t.Fatal("existing JSON config accepted")
	}
	if filepath.Ext(defaultPath()) != ".yaml" {
		t.Fatal("default config is not YAML")
	}
}
