package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCalendarVersion(t *testing.T) {
	for _, value := range []string{"2026-01-01", "2026-11-12.2", "2028-02-29.999999999"} {
		if _, valid := calendarVersion(value); !valid {
			t.Errorf("rejected calendar version %q", value)
		}
	}
	for _, value := range []string{
		"dev", "dev+abcdef", "2026.11.2", "v2026-11-02", "0999-11-02",
		"2026-1-01", "2026-11-2", "2026-13-01", "2026-11-00", "2026-11-31", "2026-02-29",
		"2026-11-02.", "2026-11-02.0", "2026-11-02.1", "2026-11-02.02",
		"2026-11-02.+2", "2026-11-02.2.1", "2026-11-02.1000000000",
	} {
		if _, valid := calendarVersion(value); valid {
			t.Errorf("accepted non-release version %q", value)
		}
	}
}

func TestCLIUpdateNotification(t *testing.T) {
	original := Version
	Version = "2026-11-09.9"
	t.Cleanup(func() { Version = original })
	name, err := cliDownloadName()
	if err != nil {
		t.Skip(err)
	}

	for _, test := range []struct {
		name    string
		version string
		status  int
		notify  bool
	}{
		{name: "newer sequence", version: "2026-11-09.10", notify: true},
		{name: "newer date", version: "2026-11-10", notify: true},
		{name: "newer month", version: "2026-12-01", notify: true},
		{name: "same version", version: "2026-11-09.9"},
		{name: "first release on same date", version: "2026-11-09"},
		{name: "older date", version: "2026-11-08.10"},
		{name: "development build", version: "dev+12345678"},
		{name: "unavailable manifest", status: http.StatusServiceUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.URL.Path != "/downloads/manifest.json" || r.Header.Get("Authorization") != "" {
					t.Error("unexpected update request path or credentials")
				}
				if test.status != 0 {
					w.WriteHeader(test.status)
					return
				}
				_ = json.NewEncoder(w).Encode(cliRelease{
					Version: test.version,
					Files:   map[string]string{name: strings.Repeat("a", 64)},
				})
			}))
			defer server.Close()

			var stdout, stderr bytes.Buffer
			client := Client{
				Profile: Profile{Endpoint: server.URL, Token: "fixture-api-token"},
				Out:     &stdout,
				Err:     &stderr,
			}
			cacheDir := t.TempDir()
			now := time.Now()
			client.notifyCLIUpdate(context.Background(), cacheDir, now)
			client.notifyCLIUpdate(context.Background(), cacheDir, now.Add(time.Hour))

			if requests.Load() != 1 || stdout.Len() != 0 {
				t.Fatalf("requests=%d stdout=%q", requests.Load(), stdout.String())
			}
			want := ""
			if test.notify {
				want = "krun " + test.version + " is available. Run krun upgrade --endpoint " + strconv.Quote(server.URL) + " to update.\n"
			}
			if stderr.String() != want {
				t.Fatalf("stderr=%q, want %q", stderr.String(), want)
			}
		})
	}
}

func TestCLIUpdateCacheIsScopedAndExclusive(t *testing.T) {
	cacheDir := t.TempDir()
	now := time.Now()
	const endpoint = "https://console.example.com"
	var checks atomic.Int32
	var done sync.WaitGroup
	for range 12 {
		done.Go(func() {
			if claimCLIUpdateCheck(cacheDir, endpoint, now) {
				checks.Add(1)
			}
		})
	}
	done.Wait()
	if checks.Load() != 1 {
		t.Fatalf("claimed %d concurrent checks", checks.Load())
	}
	if claimCLIUpdateCheck(cacheDir, endpoint, now.Add(-time.Nanosecond)) {
		t.Fatal("an earlier concurrent invocation claimed a second check")
	}
	if claimCLIUpdateCheck(cacheDir, endpoint, now.Add(cliUpdateInterval-time.Nanosecond)) {
		t.Fatal("checked within 24 hours")
	}
	if !claimCLIUpdateCheck(cacheDir, "https://other.example.com", now) {
		t.Fatal("another endpoint was throttled")
	}
	if !claimCLIUpdateCheck(cacheDir, endpoint, now.Add(cliUpdateInterval)) {
		t.Fatal("the next check was not allowed after 24 hours")
	}
}

func TestCLIUpdateSkipsNonInteractiveCommands(t *testing.T) {
	original := Version
	Version = "2026-11-01"
	t.Cleanup(func() { Version = original })
	t.Setenv("CI", "")
	t.Setenv("KRUN_TOKEN", "")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/auth/me" {
			t.Errorf("non-interactive command requested %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"user":{"name":"example"}}`))
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	args := []string{"whoami", "--endpoint", server.URL, "--profile", filepath.Join(t.TempDir(), "profile.yaml")}
	if err := Run(context.Background(), args, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !json.Valid(stdout.Bytes()) || stderr.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}
