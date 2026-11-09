package cli

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestConfigureEndpoint(t *testing.T) {
	t.Setenv("KRUN_ENDPOINT", "https://temporary.example.com")
	t.Setenv("KRUN_TOKEN", "environment-secret")
	path := filepath.Join(t.TempDir(), "nested", "profile.yaml")
	var out bytes.Buffer
	run := func(args ...string) error {
		t.Helper()
		out.Reset()
		args = append(args, "--profile", path)
		return Run(context.Background(), args, strings.NewReader(""), &out, &out)
	}
	if err := run("config", "set", "endpoint", "https://console.example.com/"); err != nil {
		t.Fatal(err)
	}
	profile, err := loadProfile(path)
	if err != nil || profile.Endpoint != "https://console.example.com" || profile.Token != "" {
		t.Fatalf("unexpected saved endpoint: %+v, %v", profile, err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(raw), "endpoint: https://console.example.com") {
		t.Fatal("endpoint was not persisted under its canonical name", err)
	}
	if err := run("config", "get", "endpoint"); err != nil || out.String() != "https://console.example.com\n" {
		t.Fatalf("get endpoint: %q, %v", out.String(), err)
	}

	profile.Organization = "org_saved"
	profile.Token = "saved-access-secret"
	profile.RefreshToken = "saved-refresh-secret"
	profile.Expires = time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	c := Client{Path: path, Profile: profile}
	if err := c.save(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := run("config", "show", "--endpoint", "https://override.example.com"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Endpoint: https://console.example.com") ||
		!strings.Contains(out.String(), "Organization: org_saved") ||
		!strings.Contains(out.String(), "Credentials saved: true") || strings.Contains(out.String(), "secret") {
		t.Fatalf("config show leaked credentials or showed an override: %q", out.String())
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("config show changed the profile", err)
	}
	if err := run("config", "set", "endpoint", "https://console.example.com/"); err != nil {
		t.Fatal(err)
	}
	same, err := loadProfile(path)
	if err != nil || same != profile {
		t.Fatal("saving the same endpoint changed credentials", err)
	}
	if err := run("config", "set", "endpoint", "https://new.example.com"); err != nil {
		t.Fatal(err)
	}
	changed, err := loadProfile(path)
	if err != nil || changed.Endpoint != "https://new.example.com" || changed.Organization != "" || changed.Token != "" || changed.RefreshToken != "" || !changed.Expires.IsZero() {
		t.Fatal("switching endpoint did not clear credentials and organization", err)
	}
}

func TestConfigureRejectsInvalidInputWithoutWriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profile.yaml")
	c := Client{Path: path, Profile: Profile{Endpoint: "https://console.example.com", Token: "saved-secret"}}
	if err := c.save(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"config", "set", "endpoint", "http://public.example.com"},
		{"config", "set", "endpoint", "https://console.example.com/api"},
		{"config", "set", "endpoint", "https://user:password@console.example.com"},
		{"config", "set", "endpoint", "https://console.example.com?token=secret"},
		{"config", "set", "endpoint"},
		{"config", "set", "token", "secret"},
		{"config", "set", "server", "https://console.example.com"},
		{"config", "get", "endpoint", "extra"},
		{"config", "set", "org", "../other"},
		{"config", "set", "org", ""},
	} {
		var out bytes.Buffer
		if err := Run(context.Background(), append(args, "--profile", path), strings.NewReader(""), &out, &out); err == nil {
			t.Fatalf("accepted invalid arguments: %v", args)
		}
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatal("invalid configuration changed the profile", err)
		}
	}
}

func TestCLIRejectsUnknownEndpointNames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profile.yaml")
	var out bytes.Buffer
	if err := Run(context.Background(), []string{"login", "--server", "https://console.example.com", "--profile", path}, strings.NewReader(""), &out, &out); err == nil {
		t.Fatal("unknown endpoint flag was accepted")
	}
	if err := os.WriteFile(path, []byte("server: https://console.example.com\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadProfile(path); err == nil {
		t.Fatal("unknown profile field was accepted")
	}
}

func TestEndpointPrecedenceDoesNotWriteProfile(t *testing.T) {
	for _, source := range []string{"profile", "environment", "endpoint flag"} {
		t.Run(source, func(t *testing.T) {
			wantAuthorization := ""
			if source == "profile" {
				wantAuthorization = "Bearer saved-secret"
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/me" || r.Header.Get("Authorization") != wantAuthorization {
					t.Error("endpoint override sent the wrong request or leaked saved credentials")
				}
				_, _ = w.Write([]byte("{}"))
			}))
			defer server.Close()
			path := filepath.Join(t.TempDir(), "profile.yaml")
			profile := Profile{Endpoint: "https://saved.example.com", Token: "saved-secret"}
			if source == "profile" {
				profile.Endpoint = server.URL
			}
			c := Client{Path: path, Profile: profile}
			if err := c.save(); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("KRUN_TOKEN", "")
			t.Setenv("KRUN_ENDPOINT", "")
			args := []string{"whoami", "--profile", path}
			switch source {
			case "environment":
				t.Setenv("KRUN_ENDPOINT", server.URL)
			case "endpoint flag":
				t.Setenv("KRUN_ENDPOINT", "https://ignored.example.com")
				args = append(args, "--endpoint", server.URL)
			}
			var out bytes.Buffer
			if err := Run(context.Background(), args, strings.NewReader(""), &out, &out); err != nil {
				t.Fatal(err)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("temporary endpoint selection changed the profile", err)
			}
		})
	}
}

func TestDefaultOrganizationScope(t *testing.T) {
	paths := make(chan string, 1)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths <- r.URL.Path
		_, _ = w.Write([]byte("{}"))
	})
	savedEndpoint := httptest.NewServer(handler)
	defer savedEndpoint.Close()
	otherEndpoint := httptest.NewServer(handler)
	defer otherEndpoint.Close()
	t.Setenv("KRUN_ENDPOINT", "")
	t.Setenv("KRUN_TOKEN", "")
	path := filepath.Join(t.TempDir(), "profile.yaml")
	run := func(args ...string) (string, error) {
		t.Helper()
		var out bytes.Buffer
		err := Run(t.Context(), append(args, "--profile", path), strings.NewReader(""), &out, &out)
		return out.String(), err
	}
	for _, args := range [][]string{
		{"config", "set", "endpoint", savedEndpoint.URL},
		{"config", "set", "org", "org-default"},
	} {
		if _, err := run(args...); err != nil {
			t.Fatal(err)
		}
	}
	if out, err := run("config", "get", "org"); err != nil || out != "org-default\n" {
		t.Fatalf("get org = %q, %v", out, err)
	}
	for _, test := range []struct {
		args []string
		path string
	}{
		{[]string{"workers", "list"}, "/api/v1/orgs/org-default/workers"},
		{[]string{"workers", "repository", "providers"}, "/api/v1/orgs/org-default/workers/repository-providers"},
		{[]string{"workers", "list", "--org", "org-explicit"}, "/api/v1/orgs/org-explicit/workers"},
		{[]string{"build", "logs", "--worker", "worker", "--build", "build"}, "/api/v1/orgs/org-default/workers/worker/builds/build/logs"},
		{[]string{"r2", "bucket", "list"}, "/api/v1/orgs/org-default/buckets"},
		{[]string{"members", "list"}, "/api/v1/orgs/org-default/members"},
		{[]string{"groups", "list", "--system"}, "/api/v1/system/groups"},
		{[]string{"audit"}, "/api/v1/orgs/org-default/audit"},
		{[]string{"audit", "--system"}, "/api/v1/system/audit"},
		{[]string{"org", "list"}, "/api/v1/orgs"},
		{[]string{"workers", "list", "--endpoint", otherEndpoint.URL, "--org", "org-other"}, "/api/v1/orgs/org-other/workers"},
	} {
		if _, err := run(test.args...); err != nil {
			t.Fatalf("%v: %v", test.args, err)
		}
		select {
		case got := <-paths:
			if got != test.path {
				t.Fatalf("%v requested %q, want %q", test.args, got, test.path)
			}
		default:
			t.Fatalf("%v made no HTTP request", test.args)
		}
	}
	for _, args := range [][]string{
		{"workers", "list", "--endpoint", otherEndpoint.URL},
		{"audit", "--system", "--org", "org-explicit"},
		{"audit", "--org="},
	} {
		if _, err := run(args...); err == nil {
			t.Fatalf("%v should fail before making a request", args)
		}
		select {
		case got := <-paths:
			t.Fatalf("%v unexpectedly requested %q", args, got)
		default:
		}
	}
	if out, err := run("config", "get", "org"); err != nil || out != "org-default\n" {
		t.Fatalf("overrides changed the saved org: %q, %v", out, err)
	}
	if _, err := run("config", "unset", "org"); err != nil {
		t.Fatal(err)
	}
	if _, err := run("workers", "list"); err == nil {
		t.Fatal("workers list accepted an unset organization")
	}
}
