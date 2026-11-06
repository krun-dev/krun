package control

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	kruncli "github.com/krun-dev/krun/internal/cli"
	"github.com/krun-dev/krun/internal/configfile"
)

type loginURLWriter struct{ target chan string }

func (w loginURLWriter) Write(p []byte) (int, error) {
	if s := string(p); strings.HasPrefix(s, "Open ") {
		select {
		case w.target <- strings.Split(strings.TrimPrefix(s, "Open "), "\n")[0]:
		default:
		}
	}
	return len(p), nil
}
func TestCLIControlOAuthLoginRefreshAndLogoutContract(t *testing.T) {
	for _, device := range []bool{false, true} {
		name := "PKCE"
		if device {
			name = "Device"
		}
		t.Run(name, func(t *testing.T) {
			s, store := testServer(t)
			oauthBrowser(t, s, store)
			httpServer := httptest.NewServer(s)
			defer httpServer.Close()
			s.Config.PublicURL = httpServer.URL
			target := make(chan string, 1)
			var out bytes.Buffer
			profilePath := filepath.Join(t.TempDir(), "profile.yaml")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			args := []string{"login", "--browser=false", "--endpoint", httpServer.URL, "--config", profilePath}
			if device {
				args = append(args, "--device")
			} else {
				args = append(args, "--callback-port", "0")
			}
			go func() { done <- kruncli.Run(ctx, args, strings.NewReader(""), &out, loginURLWriter{target}) }()
			var login string
			select {
			case login = <-target:
			case <-time.After(3 * time.Second):
				t.Fatal("login did not print its browser URL")
			}
			id := ""
			client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			if device {
				u, _ := url.Parse(login)
				id = u.Query().Get("oauth")
			} else {
				r, err := client.Get(login)
				if err != nil {
					t.Fatal(err)
				}
				r.Body.Close()
				u, _ := url.Parse(r.Header.Get("Location"))
				id = u.Query().Get("oauth")
			}
			approved := oauthApproval(s, id, true)
			if approved.Code != 200 {
				t.Fatalf("approval %d %s", approved.Code, approved.Body.String())
			}
			if !device {
				var redirect struct {
					URL string `json:"redirect_url"`
				}
				_ = json.Unmarshal(approved.Body.Bytes(), &redirect)
				tampered, _ := url.Parse(redirect.URL)
				query := tampered.Query()
				query.Set("state", "tampered-state")
				tampered.RawQuery = query.Encode()
				invalid, e := client.Get(tampered.String())
				if e != nil {
					t.Fatal(e)
				}
				invalid.Body.Close()
				if invalid.StatusCode != 403 {
					t.Fatal("CLI accepted a callback with wrong state")
				}
				r, err := client.Get(redirect.URL)
				if err != nil {
					t.Fatal(err)
				}
				r.Body.Close()
				if r.StatusCode != 200 {
					t.Fatal("loopback callback failed")
				}
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(6 * time.Second):
				t.Fatal("CLI did not finish OAuth login")
			}
			load := func() kruncli.Profile {
				raw, err := os.ReadFile(profilePath)
				if err != nil {
					t.Fatal(err)
				}
				var profile kruncli.Profile
				if err = configfile.Decode(profilePath, raw, &profile); err != nil {
					t.Fatal(err)
				}
				return profile
			}
			initial := load()
			if initial.Token == "" || initial.RefreshToken == "" {
				t.Fatal("CLI did not save rotating credentials")
			}
			if err := store.Update(context.Background(), PartitionIAM, func(st *State) error {
				session := st.Sessions[digest(initial.Token)]
				session.Absolute = now().Add(-time.Minute)
				st.Sessions[digest(initial.Token)] = session
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			var status bytes.Buffer
			if err := kruncli.Run(context.Background(), []string{"whoami", "--config", profilePath}, strings.NewReader(""), &status, &status); err != nil {
				t.Fatal("automatic refresh failed", err)
			}
			current := load()
			if current.Token == initial.Token || current.RefreshToken == initial.RefreshToken {
				t.Fatal("CLI refresh did not rotate and save")
			}
			if err := kruncli.Run(context.Background(), []string{"logout", "--config", profilePath}, strings.NewReader(""), &status, &status); err != nil {
				t.Fatal(err)
			}
			if profile := load(); profile.Token != "" || profile.RefreshToken != "" {
				t.Fatal("logout kept local credentials")
			}
			if w := request(s, "GET", "/api/auth/me", current.Token, ""); w.Code != 401 {
				t.Fatal("logout kept access valid")
			}
		})
	}
}

func TestCLIWorkerPermissionsAndAuditContract(t *testing.T) {
	s, _ := testServer(t)
	httpServer := httptest.NewServer(s)
	defer httpServer.Close()
	t.Setenv("KRUN_TOKEN", "")
	t.Setenv("KRUN_ENDPOINT", "")
	profilePath := filepath.Join(t.TempDir(), "profile.yaml")
	run := func(token string, args ...string) (string, error) {
		t.Helper()
		profile := kruncli.Profile{Endpoint: httpServer.URL, Token: token}
		raw, err := configfile.Marshal(profilePath, profile)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(profilePath, raw, 0600); err != nil {
			t.Fatal(err)
		}
		var output bytes.Buffer
		args = append(args, "--config", profilePath)
		err = kruncli.Run(context.Background(), args, strings.NewReader(""), &output, &output)
		return output.String(), err
	}
	mustRun := func(token string, args ...string) string {
		t.Helper()
		output, err := run(token, args...)
		if err != nil {
			t.Fatalf("CLI %v: %v (%s)", args, err, output)
		}
		return output
	}

	created := mustRun("krun_cli_admin", "workers", "create", "--org", "org-a", "--name", "CLI Worker")
	var worker Resource
	if err := json.Unmarshal([]byte(created), &worker); err != nil || worker.Kind != "worker" || !strings.HasPrefix(worker.ID, "worker_") {
		t.Fatalf("incorrect Worker identity: %s (%v)", created, err)
	}
	listed := mustRun("krun_cli_admin", "workers", "list", "--org", "org-a")
	if !strings.Contains(listed, worker.ID) {
		t.Fatal("created Worker missing from CLI list", listed)
	}
	mustRun("krun_cli_admin", "roles", "grant", "--org", "org-a", "--subject", "viewer", "--role", "member", "--scope-kind", "worker", "--scope-id", worker.ID)
	mustRun("krun_cli_viewer", "workers", "update", "--org", "org-a", "--id", worker.ID, "--name", "Updated Worker", "--revision", "1")
	if _, err := run("krun_cli_viewer", "workers", "update", "--org", "org-b", "--id", "svc-b", "--name", "Forbidden", "--revision", "1"); err == nil {
		t.Fatal("Worker grant escaped its organization and resource scope")
	}

	policy := Policy{Version: 1, Statements: []Statement{{Effect: "allow", Actions: []string{"worker.read"}, Resource: Selector{Kind: "worker", Organization: "org-a", IDs: []string{worker.ID}}}}}
	policyPath := filepath.Join(t.TempDir(), "policy.yaml")
	raw, err := configfile.Marshal(policyPath, policy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(policyPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	keyOutput := mustRun("krun_cli_admin", "keys", "create", "--org", "org-a", "--name", "Read Worker", "--policy", policyPath)
	var key struct {
		Token string `json:"key"`
	}
	if err := json.Unmarshal([]byte(keyOutput), &key); err != nil || key.Token == "" {
		t.Fatal("missing Worker API Key", keyOutput, err)
	}
	mustRun(key.Token, "workers", "get", "--org", "org-a", "--id", worker.ID)
	if _, err := run(key.Token, "workers", "update", "--org", "org-a", "--id", worker.ID, "--name", "Forbidden", "--revision", "2"); err == nil {
		t.Fatal("read-only Worker policy permitted a write")
	}

	for _, path := range []string{"/api/v1/orgs/org-a/services", "/api/v1/orgs/org-a/services/" + worker.ID} {
		response := request(s, http.MethodGet, path, "krun_cli_admin", "")
		if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), "RESOURCE_NOT_FOUND") {
			t.Fatal("legacy resource route remains", path, response.Code, response.Body.String())
		}
	}
	if _, err := run("krun_cli_admin", "services", "list", "--org", "org-a"); err == nil {
		t.Fatal("legacy CLI resource command remains")
	}
	if _, err := run("krun_cli_admin", "roles", "grant", "--org", "org-a", "--subject", "viewer", "--role", "member", "--scope-kind", "service", "--scope-id", worker.ID); err == nil {
		t.Fatal("legacy role scope was accepted")
	}
	legacyPolicy := `{"name":"Legacy","expires_in":3600,"policy":{"schema_version":1,"statements":[{"effect":"allow","actions":["service.read"],"resource":{"kind":"service","organization_id":"org-a","ids":["` + worker.ID + `"]},"conditions":{}}]}}`
	if response := request(s, http.MethodPost, "/api/v1/orgs/org-a/api-keys", "krun_cli_admin", legacyPolicy); response.Code != http.StatusUnprocessableEntity {
		t.Fatal("legacy policy was accepted", response.Code, response.Body.String())
	}

	page := getAuditPage(t, s, "/api/v1/orgs/org-a/audit?limit=500", "krun_cli_admin")
	createdAudit := false
	updatedAudit := false
	deniedAudit := false
	for _, entry := range page.Items {
		if strings.HasPrefix(entry.Action, "service.") {
			t.Fatal("legacy action emitted to audit", entry.Action)
		}
		createdAudit = createdAudit || entry.Action == "worker.create" && entry.Allowed
		updatedAudit = updatedAudit || entry.Action == "worker.update" && entry.Resource == worker.ID && entry.Allowed
		deniedAudit = deniedAudit || entry.Action == "worker.update" && entry.Resource == worker.ID && !entry.Allowed && entry.KeyID != ""
	}
	if !createdAudit || !updatedAudit || !deniedAudit {
		t.Fatalf("missing Worker audits: created=%v updated=%v denied=%v", createdAudit, updatedAudit, deniedAudit)
	}
}
