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
			args := []string{"login", "--browser=false", "--server", httpServer.URL, "--config", profilePath}
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
