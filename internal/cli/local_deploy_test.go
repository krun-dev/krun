package cli

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDeployLocalUpload(t *testing.T) {
	for _, tc := range []struct {
		name       string
		zip        bool
		wait       bool
		buildState string
	}{
		{name: "directory with no deployment wait", buildState: "succeeded"},
		{name: "ZIP with OAuth refresh and deployment wait", zip: true, wait: true, buildState: "succeeded"},
		{name: "failed build stops deployment", buildState: "failed"},
		{name: "build timeout stops deployment", buildState: "queued"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			source := filepath.Join(dir, "site")
			fixtures := map[string]string{
				"index.html":                   "<h1>Hello</h1>",
				"assets/app.js":                "console.log('hello')",
				".git/HEAD":                    "ignored",
				"worktree/.git":                "ignored",
				"nested/node_modules/pkg/file": "ignored",
				".wrangler/tmp/file":           "ignored",
			}
			for name, data := range fixtures {
				writeLocalDeployFixture(t, filepath.Join(source, filepath.FromSlash(name)), []byte(data))
			}
			if err := os.Symlink(filepath.Join(source, "index.html"), filepath.Join(source, "alias.html")); err != nil && os.PathSeparator == '/' {
				t.Fatal(err)
			}

			projectRoot := ""
			wantFiles := map[string]string{
				"file:index.html/index.html": "<h1>Hello</h1>",
				"file:assets/app.js/app.js":  "console.log('hello')",
			}
			if tc.zip {
				var raw bytes.Buffer
				archive := zip.NewWriter(&raw)
				entry, err := archive.Create("worker/project/wrangler.json")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := io.WriteString(entry, `{"main":"src/index.js"}`); err != nil {
					t.Fatal(err)
				}
				if err := archive.Close(); err != nil {
					t.Fatal(err)
				}
				source = filepath.Join(dir, "worker.ZIP")
				writeLocalDeployFixture(t, source, raw.Bytes())
				projectRoot = "worker/project"
				wantFiles = map[string]string{"archive/worker.ZIP": raw.String()}
			}

			const worker = "/api/v1/orgs/org-a/workers/wrk_a"
			var calls []string
			var buildKey string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				call := r.Method + " " + strings.TrimPrefix(r.URL.Path, worker)
				calls = append(calls, call)
				w.Header().Set("Content-Type", "application/json")
				switch call {
				case "POST /environments/production/local-builds":
					if r.URL.Query().Get("project_root") != projectRoot {
						t.Errorf("project root query = %q, want %q", r.URL.RawQuery, projectRoot)
					}
					key := r.Header.Get("Idempotency-Key")
					if key == "" || key == "release-retry" || buildKey != "" && key != buildKey {
						t.Errorf("invalid build key: %q (previous key: %q)", key, buildKey)
					}
					buildKey = key
					form, err := r.MultipartReader()
					if err != nil {
						t.Error(err)
						http.Error(w, "invalid multipart", http.StatusBadRequest)
						return
					}
					got := make(map[string]string)
					for {
						part, err := form.NextPart()
						if err == io.EOF {
							break
						}
						if err != nil {
							t.Error(err)
							return
						}
						data, err := io.ReadAll(part)
						if err != nil {
							t.Error(err)
							return
						}
						got[part.FormName()+"/"+part.FileName()] = string(data)
					}
					if !reflect.DeepEqual(got, wantFiles) {
						t.Errorf("uploaded files = %v, want %v", got, wantFiles)
					}
					if tc.zip && r.Header.Get("Authorization") == "Bearer krun_cli_stale" {
						w.WriteHeader(http.StatusUnauthorized)
						fmt.Fprint(w, `{"error":{"code":"UNAUTHENTICATED","message":"refresh required"}}`)
						return
					}
					wantAuth := "Bearer fixture-ci-key"
					if tc.zip {
						wantAuth = "Bearer krun_cli_fresh"
					}
					if r.Header.Get("Authorization") != wantAuth || r.Header.Get("Accept-Language") != "en" {
						t.Error("local build lost request authentication or language")
					}
					w.WriteHeader(http.StatusAccepted)
					fmt.Fprint(w, `{"kind":"static","build":{"id":"bld_fixture","state":"queued"}}`)
				case "POST /api/auth/oauth/token":
					if err := r.ParseForm(); err != nil || r.Form.Get("refresh_token") != "krun_refresh_stale" {
						t.Errorf("invalid refresh request: %v", err)
					}
					fmt.Fprint(w, `{"token_type":"Bearer","access_token":"krun_cli_fresh","refresh_token":"krun_refresh_fresh","expires_in":3600}`)
				case "GET /builds/bld_fixture":
					fmt.Fprintf(w, `{"id":"bld_fixture","state":%q,"error":"compile error"}`, tc.buildState)
				case "POST /environments/production/deployments":
					var input struct {
						BuildID  string `json:"build_id"`
						Key      string `json:"idempotency_key"`
						Revision int    `json:"expected_revision"`
					}
					if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input.BuildID != "bld_fixture" || input.Key != "release-retry" || input.Revision != 7 {
						t.Errorf("invalid deployment input: %+v, %v", input, err)
					}
					fmt.Fprint(w, `{"id":"op_fixture","state":"queued"}`)
				case "GET /operations/op_fixture":
					fmt.Fprint(w, `{"id":"op_fixture","state":"available"}`)
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()

			t.Setenv("KRUN_ENDPOINT", "")
			t.Setenv("KRUN_TOKEN", "fixture-ci-key")
			profile := filepath.Join(dir, "profile.yaml")
			profileData := "endpoint: " + server.URL + "\norg: org-a\n"
			if tc.zip {
				t.Setenv("KRUN_TOKEN", "")
				profileData += "token: krun_cli_stale\nrefresh_token: krun_refresh_stale\n"
			}
			writeLocalDeployFixture(t, profile, []byte(profileData))
			t.Chdir(dir)
			localPath := "." + string(os.PathSeparator) + filepath.Base(source)
			args := []string{
				"deploy", "--worker", "wrk_a", "--env", "production", "--profile", profile,
				"--path", localPath, "--idempotency-key", "release-retry", "--revision", "7",
			}
			if projectRoot != "" {
				args = append(args, "--project-root", projectRoot)
			}
			if !tc.wait {
				args = append(args, "--wait=false")
			}
			if tc.buildState == "queued" {
				args = append(args, "--build-timeout", "250ms")
			}

			var out, diagnostics bytes.Buffer
			err := Run(t.Context(), args, strings.NewReader(""), &out, &diagnostics)
			wantCalls := []string{"POST /environments/production/local-builds"}
			if tc.zip {
				wantCalls = append(wantCalls, "POST /api/auth/oauth/token", "POST /environments/production/local-builds")
			}
			wantCalls = append(wantCalls, "GET /builds/bld_fixture")
			switch tc.buildState {
			case "failed":
				if err == nil || !strings.Contains(err.Error(), "compile error") || out.Len() != 0 {
					t.Fatalf("build failure: %v, stdout: %s", err, &out)
				}
			case "queued":
				if !errors.Is(err, context.DeadlineExceeded) || out.Len() != 0 {
					t.Fatalf("build timeout: %v, stdout: %s", err, &out)
				}
			default:
				if err != nil || !strings.Contains(out.String(), `"id": "op_fixture"`) {
					t.Fatalf("deploy: %v, stdout: %s, diagnostics: %s", err, &out, &diagnostics)
				}
				wantCalls = append(wantCalls, "POST /environments/production/deployments")
				if tc.wait {
					wantCalls = append(wantCalls, "GET /operations/op_fixture")
				}
			}
			if !reflect.DeepEqual(calls, wantCalls) {
				t.Fatalf("calls = %v, want %v", calls, wantCalls)
			}
			if !strings.Contains(diagnostics.String(), "Build ID: bld_fixture") {
				t.Fatalf("missing build ID in diagnostics: %s", &diagnostics)
			}
			if !tc.zip && tc.buildState == "succeeded" {
				if err := Run(t.Context(), args, strings.NewReader(""), io.Discard, io.Discard); err != nil {
					t.Fatalf("retry release: %v", err)
				}
			}
		})
	}
}

func TestDeployRejectsUnsupportedLocalFiles(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("invalid local path made an HTTP request: %s %s", r.Method, r.URL)
	}))
	defer server.Close()
	t.Setenv("KRUN_ENDPOINT", server.URL)
	t.Setenv("KRUN_TOKEN", "fixture-ci-key")

	dir := t.TempDir()
	plain := filepath.Join(dir, "index.html")
	writeLocalDeployFixture(t, plain, []byte("<h1>Hello</h1>"))
	large := filepath.Join(dir, "large.zip")
	writeLocalDeployFixture(t, large, nil)
	if err := os.Truncate(large, (32<<20)+1); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		path string
		want string
	}{
		{filepath.Join(dir, "missing"), "missing"},
		{plain, "a directory or a .zip file"},
		{large, "32 MiB"},
	}
	if os.PathSeparator == '/' {
		name := filepath.Join(dir, "bad\nfilename.zip")
		writeLocalDeployFixture(t, name, nil)
		tests = append(tests, struct{ path, want string }{name, "line breaks"})
	}
	for _, tc := range tests {
		t.Run(filepath.Base(tc.path), func(t *testing.T) {
			args := []string{
				"deploy", "--org", "org-a", "--worker", "wrk_a", "--env", "production",
				"--revision", "7", "--path", tc.path, "--profile", filepath.Join(dir, "profile.yaml"),
			}
			err := Run(t.Context(), args, strings.NewReader(""), io.Discard, io.Discard)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("invalid local source: got %v, want %q", err, tc.want)
			}
		})
	}
}

func writeLocalDeployFixture(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}
