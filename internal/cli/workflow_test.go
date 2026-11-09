package cli

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/krun-dev/krun/internal/domain"
)

func TestRemovedCommandAliases(t *testing.T) {
	config := filepath.Join(t.TempDir(), "profile.yaml")
	for _, args := range [][]string{{"auth", "status"}, {"status"}, {"orgs", "list"}, {"version"}, {"help"}, {"login", "--no-browser"}} {
		var out bytes.Buffer
		if err := Run(t.Context(), append(args, "--profile", config), strings.NewReader(""), &out, &out); err == nil {
			t.Fatalf("accepted removed alias %v", args)
		}
	}
}

func TestLogoutRejectsTemporaryCredentials(t *testing.T) {
	config := filepath.Join(t.TempDir(), "profile.yaml")
	saved := []byte("endpoint: https://console.example.com\ntoken: krun_key_saved_fixture\n")
	if err := os.WriteFile(config, saved, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KRUN_ENDPOINT", "")
	t.Setenv("KRUN_TOKEN", "krun_key_temporary_fixture")
	if err := Run(t.Context(), []string{"logout", "--profile", config}, strings.NewReader(""), io.Discard, io.Discard); err == nil {
		t.Fatal("logout accepted temporary credentials")
	}
	actual, err := os.ReadFile(config)
	if err != nil || !bytes.Equal(actual, saved) {
		t.Fatalf("logout changed the saved profile (%v)", err)
	}
}

func TestR2TransfersPreserveBytesAndKeepControlCredentialsPrivate(t *testing.T) {
	var raw bytes.Buffer
	compressor := gzip.NewWriter(&raw)
	if _, err := compressor.Write([]byte("compressed object")); err != nil {
		t.Fatal(err)
	}
	if err := compressor.Close(); err != nil {
		t.Fatal(err)
	}
	object := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("received control-plane credentials")
		}
		if r.Method == http.MethodPut {
			data, err := io.ReadAll(r.Body)
			if err != nil || !bytes.Equal(data, raw.Bytes()) || r.ContentLength != int64(raw.Len()) {
				t.Errorf("upload changed object bytes or length (%v)", err)
			}
			if r.Header.Get("X-Test-Signed") != "value" {
				t.Error("missing signed header")
			}
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(raw.Bytes())
	}))
	defer object.Close()
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer krun_key_fixture" ||
			r.URL.Path != "/api/v1/orgs/org-a/buckets/bkt_a/objects/access" {
			t.Errorf("unexpected control request: %s", r.URL.Path)
		}
		var input domain.BucketObjectAccessInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input.Key != "a/b + c" {
			t.Errorf("unexpected object request: %+v (%v)", input, err)
		}
		grant := domain.BucketObjectAccess{Key: input.Key, URL: object.URL, Method: http.MethodGet}
		switch input.Operation {
		case "upload":
			grant.Method = http.MethodPut
			grant.Headers = map[string]string{"X-Test-Signed": "value"}
		case "download":
		default:
			t.Errorf("unexpected grant operation %q", input.Operation)
		}
		_ = json.NewEncoder(w).Encode(grant)
	}))
	defer control.Close()
	t.Setenv("KRUN_ENDPOINT", control.URL)
	t.Setenv("KRUN_TOKEN", "krun_key_fixture")
	dir := t.TempDir()
	source := filepath.Join(dir, "source.gz")
	if err := os.WriteFile(source, raw.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	run := func(action string, options ...string) error {
		t.Helper()
		args := []string{"r2", "object", action, "bkt_a/a/b + c", "--org", "org-a", "--profile", filepath.Join(dir, "profile.yaml")}
		out.Reset()
		return Run(t.Context(), append(args, options...), bytes.NewReader(raw.Bytes()), &out, io.Discard)
	}
	if err := run("put", "--file", source); err != nil {
		t.Fatal(err)
	}
	if err := run("put", "--pipe"); err != nil {
		t.Fatal(err)
	}
	if err := run("get", "--pipe"); err != nil || !bytes.Equal(out.Bytes(), raw.Bytes()) {
		t.Fatalf("download changed bytes (%v)", err)
	}
	if err := run("get", "--file", source); !os.IsExist(err) {
		t.Fatalf("download should refuse to overwrite an existing file (%v)", err)
	}
	if err := run("url"); err != nil || !strings.Contains(out.String(), object.URL) {
		t.Fatalf("expected a signed download URL: %v", err)
	}
	if err := run("url", "--delivery"); err == nil {
		t.Fatal("accepted an unsupported delivery option")
	}
}

func TestDeployWaitsForRuntimeConfirmation(t *testing.T) {
	const base = "/api/v1/orgs/org-a/workers/w"
	var reads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "POST " + base + "/environments/production/deployments":
			_, _ = io.WriteString(w, `{"id":"op-a","state":"queued"}`)
		case "GET " + base + "/operations/op-a":
			if reads.Add(1) == 1 {
				_, _ = io.WriteString(w, `{"id":"op-a","state":"committed"}`)
				return
			}
			_, _ = io.WriteString(w, `{"id":"op-a","state":"available"}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	t.Setenv("KRUN_ENDPOINT", server.URL)
	t.Setenv("KRUN_TOKEN", "")
	var out, diagnostics bytes.Buffer
	args := []string{
		"deploy", "--org", "org-a", "--worker", "w", "--env", "production",
		"--build", "bld-a", "--revision", "0", "--timeout", "10s",
		"--profile", filepath.Join(t.TempDir(), "profile.yaml"),
	}
	if err := Run(t.Context(), args, strings.NewReader(""), &out, &diagnostics); err != nil {
		t.Fatalf("deploy failed: %v (%s)", err, &diagnostics)
	}
	if reads.Load() != 2 || !strings.Contains(out.String(), `"state": "available"`) || !strings.Contains(diagnostics.String(), "Operation ID: op-a") {
		t.Fatalf("incorrect wait result: %s (%s)", &out, &diagnostics)
	}
}
