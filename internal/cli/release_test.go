package cli

import (
	"bytes"
	"encoding/json"
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

func TestRepositoryRelease(t *testing.T) {
	for _, tc := range []struct {
		name   string
		args   []string
		failed bool
		reuse  bool
		wantID string
	}{
		{"deploy waits for runtime", []string{"deploy", "--idempotency-key", "release-retry"}, false, false, "op_fixture"},
		{"deploy reuses an existing build", []string{"deploy", "--idempotency-key", "release-retry", "--build", "bld_fixture"}, false, true, "op_fixture"},
		{"version upload leaves traffic alone", []string{"versions", "upload", "--ref", "commit-fixture"}, false, false, "ver_fixture"},
		{"build failure stops deployment", []string{"deploy"}, true, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const worker = "/api/v1/orgs/org-a/workers/wrk_a"
			var calls []string
			var buildKey string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.Method+" "+strings.TrimPrefix(r.URL.Path, worker))
				if r.Header.Get("Authorization") != "Bearer fixture-ci-key" {
					t.Error("release lost authentication")
				}
				var input map[string]any
				if r.Method == "POST" {
					if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
						t.Error(err)
					}
				}
				w.Header().Set("Content-Type", "application/json")
				switch r.Method + " " + strings.TrimPrefix(r.URL.Path, worker) {
				case "GET /environments":
					fmt.Fprint(w, `{"items":[{"name":"production","deployment_revision":7}]}`)
				case "GET /repository":
					fmt.Fprint(w, `{"source":{"provider":"github","visibility":"private"},"revision":5}`)
				case "POST /builds":
					if input["environment"] != "production" || input["source_type"] != "repository" || input["repository_revision"] != float64(5) {
						t.Errorf("invalid build input: %v", input)
					}
					if tc.wantID == "ver_fixture" && input["ref"] != "commit-fixture" {
						t.Errorf("missing ref: %v", input)
					}
					key, _ := input["idempotency_key"].(string)
					if key == "" || (buildKey != "" && buildKey != key) {
						t.Errorf("unstable build key: %q then %q", buildKey, key)
					}
					buildKey = key
					fmt.Fprint(w, `{"id":"bld_fixture","state":"queued"}`)
				case "GET /builds/bld_fixture":
					if tc.failed {
						fmt.Fprint(w, `{"id":"bld_fixture","state":"failed","error":"compile error"}`)
					} else {
						fmt.Fprint(w, `{"id":"bld_fixture","state":"succeeded"}`)
					}
				case "POST /environments/production/versions":
					if input["build_id"] != "bld_fixture" {
						t.Errorf("invalid version input: %v", input)
					}
					fmt.Fprint(w, `{"id":"ver_fixture"}`)
				case "POST /environments/production/deployments":
					if input["build_id"] != "bld_fixture" || input["idempotency_key"] != "release-retry" || input["expected_revision"] != float64(7) {
						t.Errorf("invalid deployment input: %v", input)
					}
					fmt.Fprint(w, `{"id":"op_fixture","state":"queued"}`)
				case "GET /operations/op_fixture":
					fmt.Fprint(w, `{"id":"op_fixture","state":"available"}`)
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			t.Setenv("KRUN_TOKEN", "fixture-ci-key")
			t.Setenv("KRUN_ENDPOINT", server.URL)
			profile := filepath.Join(t.TempDir(), "profile.yaml")
			if err := os.WriteFile(profile, []byte("endpoint: "+server.URL+"\norg: org-a\n"), 0600); err != nil {
				t.Fatal(err)
			}
			args := append(tc.args, "--worker", "wrk_a", "--env", "production", "--profile", profile)
			var out, diagnostics bytes.Buffer
			err := Run(t.Context(), args, strings.NewReader(""), &out, &diagnostics)
			var wantCalls []string
			if tc.args[0] == "deploy" {
				wantCalls = append(wantCalls, "GET /environments")
			}
			if !tc.reuse {
				wantCalls = append(wantCalls, "GET /repository", "POST /builds", "GET /builds/bld_fixture")
			}
			if tc.failed {
				if err == nil || !strings.Contains(err.Error(), "compile error") || out.Len() != 0 {
					t.Fatalf("failed build: %v, stdout: %s", err, &out)
				}
			} else {
				if err != nil {
					t.Fatalf("release: %v, diagnostics: %s", err, &diagnostics)
				}
				var result struct {
					ID string `json:"id"`
				}
				if err := json.Unmarshal(out.Bytes(), &result); err != nil || result.ID != tc.wantID {
					t.Fatalf("expected only the final result: %s (%v)", &out, err)
				}
				if tc.wantID == "op_fixture" {
					wantCalls = append(wantCalls, "POST /environments/production/deployments", "GET /operations/op_fixture")
				} else {
					wantCalls = append(wantCalls, "POST /environments/production/versions")
				}
			}
			if !reflect.DeepEqual(calls, wantCalls) {
				t.Fatalf("calls = %v, want %v", calls, wantCalls)
			}
			if !tc.reuse && !strings.Contains(diagnostics.String(), "Build ID: bld_fixture") {
				t.Fatalf("missing build ID in diagnostics: %s", &diagnostics)
			}
			if tc.wantID == "op_fixture" && !tc.reuse {
				calls = nil
				if err := Run(t.Context(), args, strings.NewReader(""), io.Discard, io.Discard); err != nil {
					t.Fatalf("retry release: %v", err)
				}
				if !reflect.DeepEqual(calls, wantCalls) {
					t.Fatalf("retry calls = %v, want %v", calls, wantCalls)
				}
			}
		})
	}
}

func TestDeployRejectsUnsupportedOptions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("invalid flags should fail before HTTP: %s %s", r.Method, r.URL)
	}))
	defer server.Close()
	t.Setenv("KRUN_ENDPOINT", server.URL)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"versions", "deploy", "ver_fixture@50%"}, "total 100%"},
		{[]string{"versions", "deploy", "ver_a@50%", "ver_b@49.9%"}, "total 100%"},
		{[]string{"versions", "deploy", "ver_a@50%", "ver_a@50%"}, "distinct"},
		{[]string{"versions", "deploy", "ver_a@99.999%", "ver_b@0.001%"}, "0.01%"},
		{[]string{"versions", "deploy", "ver_a@95%foo", "ver_b"}, "invalid percentage"},
		{[]string{"versions", "deploy", "ver_a", "ver_b", "ver_c"}, "one or two"},
		{[]string{"deploy", "--dry-run", "--build", "bld_fixture"}, "cannot combine"},
		{[]string{"deploy", "--dry-run", "--path", "./dist"}, "cannot combine"},
		{[]string{"deploy", "--path", "./dist", "--build", "bld_fixture"}, "cannot be combined with --path"},
		{[]string{"deploy", "--path", "./dist", "--ref", "main"}, "cannot be combined with --path"},
		{[]string{"deploy", "--path", "./dist", "--repository-revision", "0"}, "cannot be combined with --path"},
		{[]string{"deploy", "--project-root", "site"}, "requires --path"},
		{[]string{"deploy", "--path="}, "--path is required"},
		{[]string{"deploy", "--build", "bld_fixture", "--ref", "main"}, "cannot be combined with --build"},
		{[]string{"deploy", "--config", "wrangler.jsonc"}, "require --dry-run"},
		{[]string{"whoami", "--config", "credentials.yaml"}, "use --profile"},
	} {
		args := append(tc.args, "--profile", filepath.Join(t.TempDir(), "profile.yaml"))
		if err := Run(t.Context(), args, strings.NewReader(""), io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: got %v, want %q", tc.args, err, tc.want)
		}
	}
}

func TestDeployDryRunUsesWranglerProject(t *testing.T) {
	if os.PathSeparator != '/' {
		t.Skip("fixture uses a POSIX shell")
	}
	dir := t.TempDir()
	npx := filepath.Join(dir, "npx")
	script := `#!/bin/sh
set -eu
test -z "${KRUN_TOKEN+x}"
test -z "${CLOUDFLARE_API_TOKEN+x}"
test "$WRANGLER_SEND_METRICS" = false
printf '%s\n' "$@"
`
	if err := os.WriteFile(npx, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("KRUN_TOKEN", "fixture-ci-key")
	t.Setenv("CLOUDFLARE_API_TOKEN", "fixture-cloudflare-key")
	t.Setenv("KRUN_ENDPOINT", "invalid-for-a-remote-command")
	config := filepath.Join(dir, "wrangler.jsonc")
	outdir := filepath.Join(dir, "out")
	profile := filepath.Join(dir, "profile.yaml")
	if err := os.WriteFile(profile, []byte("broken: ["), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	args := []string{"deploy", "--dry-run", "--config", config, "--outdir", outdir, "--profile", profile}
	if err := Run(t.Context(), args, strings.NewReader(""), &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{"--yes", dryRunWrangler, "deploy", "--dry-run", "--config", config, "--outdir", outdir, ""}, "\n")
	if out.String() != want {
		t.Fatalf("Wrangler arguments: got %q, want %q", out.String(), want)
	}
}

func TestDeployVersionPercentages(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"single", []string{"stable"}, `"version_id":"stable"`},
		{"split and message", []string{"stable@94.875%", "candidate@5.125", "-y", "--message", "Start rollout"}, `"message":"Start rollout","versions":[{"version_id":"stable","percentage":94.875},{"version_id":"candidate","percentage":5.125}]`},
		{"omitted weight", []string{"--yes", "stable", "--worker", "wrk_a", "candidate@0%"}, `"versions":[{"version_id":"stable","percentage":100},{"version_id":"candidate","percentage":0}]`},
		{"equal split", []string{"stable", "candidate"}, `"versions":[{"version_id":"stable","percentage":50},{"version_id":"candidate","percentage":50}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != "/api/v1/orgs/org-a/workers/wrk_a/environments/production/deployments" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL)
				}
				raw, err := io.ReadAll(r.Body)
				if err != nil || !strings.Contains(string(raw), tc.want) {
					t.Errorf("deployment request = %s, err = %v, want %s", raw, err, tc.want)
				}
				fmt.Fprint(w, `{"id":"op_fixture","state":"queued"}`)
			}))
			defer server.Close()
			t.Setenv("KRUN_ENDPOINT", server.URL)
			t.Setenv("KRUN_TOKEN", "fixture-ci-key")
			args := append([]string{"versions", "deploy"}, tc.args...)
			args = append(args, "--org", "org-a", "--worker", "wrk_a", "--env", "production", "--revision", "7", "--wait=false", "--profile", filepath.Join(t.TempDir(), "profile.yaml"))
			if err := Run(t.Context(), args, strings.NewReader(""), io.Discard, io.Discard); err != nil {
				t.Fatal(err)
			}
		})
	}
}
