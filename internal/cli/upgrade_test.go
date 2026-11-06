package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestUpgradeExecutable(t *testing.T) {
	name, err := cliDownloadName()
	if err != nil {
		t.Skip(err)
	}
	old := []byte("installed executable")
	updated := []byte("published executable")
	digest := sha256.Sum256(updated)
	release := cliRelease{
		Version: "v2",
		Files:   map[string]string{name: hex.EncodeToString(digest[:])},
	}
	for _, tc := range []struct {
		name       string
		check      bool
		existing   []byte
		body       []byte
		manifest   cliRelease
		status     int
		wantError  string
		wantOutput string
		wantBinary bool
		wantUpdate bool
	}{
		{
			name:       "upgrade",
			existing:   old,
			body:       updated,
			status:     200,
			manifest:   release,
			wantOutput: "Upgraded krun to v2",
			wantBinary: true,
			wantUpdate: true,
		},
		{
			name:       "check",
			check:      true,
			existing:   old,
			status:     200,
			manifest:   release,
			wantOutput: "Published: v2",
		},
		{
			name:       "already installed",
			existing:   updated,
			status:     200,
			manifest:   release,
			wantOutput: "up to date",
		},
		{
			name:       "checksum mismatch",
			existing:   old,
			body:       []byte("corrupt download"),
			status:     200,
			manifest:   release,
			wantError:  "checksum mismatch",
			wantBinary: true,
		},
		{
			name:       "missing binary",
			existing:   old,
			status:     404,
			manifest:   release,
			wantError:  "HTTP 404",
			wantBinary: true,
		},
		{
			name:      "missing platform",
			existing:  old,
			manifest:  cliRelease{Version: "v2", Files: map[string]string{}},
			wantError: "no valid release",
		},
		{
			name:     "invalid checksum",
			existing: old,
			manifest: cliRelease{
				Version: "v2",
				Files:   map[string]string{name: "not-a-checksum"},
			},
			wantError: "no valid release",
		},
		{
			name:      "missing version",
			existing:  old,
			manifest:  cliRelease{Files: map[string]string{name: hex.EncodeToString(digest[:])}},
			wantError: "no valid release",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			executable := filepath.Join(dir, "krun")
			if err := os.WriteFile(executable, tc.existing, 0755); err != nil {
				t.Fatal(err)
			}
			binaryRequests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
					t.Error("credentials leaked to CLI downloads")
				}
				if r.Header.Get("Cache-Control") != "no-cache" {
					t.Error("CLI download did not revalidate cache")
				}
				switch r.URL.Path {
				case "/downloads/manifest.json":
					_ = json.NewEncoder(w).Encode(tc.manifest)
				case "/downloads/" + name:
					binaryRequests++
					w.WriteHeader(tc.status)
					_, _ = w.Write(tc.body)
				default:
					t.Errorf("unexpected download: %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			var out bytes.Buffer
			c := Client{Profile: Profile{Endpoint: server.URL, Token: "saved-secret", RefreshToken: "refresh-secret"}, Out: &out}
			err := c.upgradeExecutable(context.Background(), executable, tc.check)
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("expected %q, got %v", tc.wantError, err)
				}
			} else if err != nil || !strings.Contains(out.String(), tc.wantOutput) {
				t.Fatalf("unexpected result: %q, %v", out.String(), err)
			}
			if (binaryRequests > 0) != tc.wantBinary {
				t.Fatalf("unexpected binary requests: %d", binaryRequests)
			}
			data, err := os.ReadFile(executable)
			if err != nil {
				t.Fatal(err)
			}
			want := tc.existing
			if tc.wantUpdate {
				want = updated
			}
			if !bytes.Equal(data, want) {
				t.Fatalf("installed executable changed unexpectedly: %q", data)
			}
			info, err := os.Stat(executable)
			if err != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0755 {
				t.Fatal("executable permissions lost", err)
			}
			leftovers, err := filepath.Glob(filepath.Join(dir, ".krun-upgrade-*"))
			if err != nil || len(leftovers) != 0 {
				t.Fatal("temporary downloads left behind", leftovers, err)
			}
			if c.Profile.Token != "saved-secret" || c.Profile.RefreshToken != "refresh-secret" {
				t.Fatal("upgrade modified credentials")
			}
		})
	}
}

func TestUpgradeRejectsBadDownloads(t *testing.T) {
	for _, mode := range []string{"redirect", "html", "not-found", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			targetRequests := 0
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				targetRequests++
			}))
			defer target.Close()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch mode {
				case "redirect":
					http.Redirect(w, r, target.URL, http.StatusFound)
				case "not-found":
					http.NotFound(w, r)
				default:
					_, _ = w.Write([]byte("<html>console fallback</html>"))
				}
			}))
			defer server.Close()
			var out bytes.Buffer
			c := Client{Profile: Profile{Endpoint: server.URL}, Out: &out}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "canceled" {
				cancel()
			}
			if err := c.upgradeExecutable(ctx, "unused", false); err == nil {
				t.Fatal("accepted invalid download")
			}
			if targetRequests != 0 {
				t.Fatal("followed a download redirect")
			}
		})
	}
}

func TestVersionDoesNotRequireAProfile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid.yaml")
	if err := os.WriteFile(path, []byte("invalid: ["), 0600); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"version", "--version"} {
		var out bytes.Buffer
		if err := Run(context.Background(), []string{command, "--config", path}, strings.NewReader(""), &out, &out); err != nil {
			t.Fatal(err)
		}
		if out.String() != "krun "+Version+"\n" {
			t.Fatal(out.String())
		}
	}
}
