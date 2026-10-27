package control

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	kruncli "github.com/krun-dev/krun/internal/cli"
	"github.com/krun-dev/krun/internal/configfile"
)

// This is a package-level contract test: the real CLI talks over HTTP to the
// real control handler. It catches path drift between the two packages as well
// as regressions where polling succeeds but returns an empty credential.
func TestCLIControlDeviceLoginAndLogoutContract(t *testing.T) {
	s, store := testServer(t)
	started := make(chan struct{})
	var startedOnce sync.Once
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.ServeHTTP(w, r)
		if r.URL.Path == "/api/auth/cli/start" {
			startedOnce.Do(func() { close(started) })
		}
	}))
	defer httpServer.Close()
	s.Config.PublicURL = httpServer.URL

	browserToken := "browser-admin"
	const browserCSRF = "browser-csrf"
	if err := store.Update(context.Background(), PartitionIAM, func(st *State) error {
		at := now()
		st.Sessions[digest(browserToken)] = Session{User: "admin", Hash: digest(browserToken), CSRF: browserCSRF, AuthVersion: 1, Created: at, Seen: at, Absolute: at.Add(time.Hour)}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	profilePath := filepath.Join(t.TempDir(), "profile.yaml")
	var out, errOut bytes.Buffer
	loginDone := make(chan error, 1)
	loginContext, cancelLogin := context.WithCancel(context.Background())
	defer cancelLogin()
	go func() {
		loginDone <- kruncli.Run(loginContext, []string{"login", "--no-browser", "--server", httpServer.URL, "--config", profilePath}, strings.NewReader(""), &out, &errOut)
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("CLI did not call /api/auth/cli/start")
	}
	var userCode string
	if err := store.View(context.Background(), PartitionIAM, func(st *State) error {
		for _, device := range st.Devices {
			userCode = device.UserCode
			break
		}
		return nil
	}); err != nil || userCode == "" {
		t.Fatal("device code was not stored", err)
	}

	approveBody := fmt.Sprintf(`{"user_code":%q}`, userCode)
	approve, err := http.NewRequest(http.MethodPost, httpServer.URL+"/api/auth/cli/approve", strings.NewReader(approveBody))
	if err != nil {
		t.Fatal(err)
	}
	approve.Header.Set("Content-Type", "application/json")
	approve.Header.Set("Origin", httpServer.URL)
	approve.Header.Set("X-CSRF-Token", browserCSRF)
	approve.AddCookie(&http.Cookie{Name: s.cookieName("session"), Value: browserToken})
	response, err := http.DefaultClient.Do(approve)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatal("browser approval failed", response.StatusCode)
	}

	select {
	case err = <-loginDone:
		if err != nil {
			t.Fatal("CLI device login failed", err, errOut.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("CLI did not poll /api/auth/cli/poll")
	}

	raw, err := os.ReadFile(profilePath)
	if err != nil {
		t.Fatal(err)
	}
	var profile kruncli.Profile
	if err = configfile.Decode(profilePath, raw, &profile); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(profile.Token, "krun_cli_") {
		t.Fatal("CLI saved an empty or invalid device token")
	}
	issuedToken := profile.Token
	me, err := http.NewRequest(http.MethodGet, httpServer.URL+"/api/auth/me", nil)
	if err != nil {
		t.Fatal(err)
	}
	me.Header.Set("Authorization", "Bearer "+issuedToken)
	meResponse, err := http.DefaultClient.Do(me)
	if err != nil {
		t.Fatal(err)
	}
	meResponse.Body.Close()
	if meResponse.StatusCode != http.StatusOK {
		t.Fatalf("polled CLI token is not an authenticated session: status=%d", meResponse.StatusCode)
	}

	out.Reset()
	errOut.Reset()
	if err = kruncli.Run(context.Background(), []string{"logout", "--config", profilePath}, strings.NewReader(""), &out, &errOut); err != nil {
		t.Fatal("CLI logout failed", err, errOut.String())
	}
	me, err = http.NewRequest(http.MethodGet, httpServer.URL+"/api/auth/me", nil)
	if err != nil {
		t.Fatal(err)
	}
	me.Header.Set("Authorization", "Bearer "+issuedToken)
	meResponse, err = http.DefaultClient.Do(me)
	if err != nil {
		t.Fatal(err)
	}
	defer meResponse.Body.Close()
	if meResponse.StatusCode != http.StatusUnauthorized {
		var body map[string]any
		_ = json.NewDecoder(meResponse.Body).Decode(&body)
		t.Fatalf("/api/auth/logout did not revoke the CLI session: status=%d body=%v", meResponse.StatusCode, body)
	}
}
