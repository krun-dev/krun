package control

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func repositoryTestProviders() []WorkerRepositoryProvider {
	return []WorkerRepositoryProvider{
		{ID: "github", Type: "github", Name: "GitHub", Enabled: true, Priority: 20, BaseURL: "https://github.com"},
		{ID: "company", Type: "gitlab", Name: "Company GitLab", Enabled: true, Priority: 10, BaseURL: "https://gitlab.example.com", Token: "private-test-token", AllowedRepositories: []string{"team/nested/worker"}},
		{ID: "disabled", Type: "gitlab", Enabled: false, BaseURL: "https://gitlab.com"},
	}
}

func TestRepositoryProviderConfigurationAndDisplay(t *testing.T) {
	s, _, _ := workerTestServer(t)
	s.Config.Workers.RepositoryProviders = repositoryTestProviders()
	if err := s.Config.Workers.validateRepositoryProviders(); err != nil {
		t.Fatal(err)
	}
	r := request(s, "GET", "/api/v1/orgs/org-a/workers/repository-providers", "krun_cli_admin", "")
	var out struct {
		Items []WorkerRepositoryProviderInfo `json:"items"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if r.Code != 200 || len(out.Items) != 2 || out.Items[0].ID != "company" || out.Items[1].ID != "github" {
		t.Fatal(r.Code, r.Body.String())
	}
	if strings.Contains(r.Body.String(), "token") || strings.Contains(r.Body.String(), "allowed_repositories") || strings.Contains(r.Body.String(), "private-test") {
		t.Fatal("provider credentials or policy leaked")
	}
	for _, change := range []func(*WorkersConfig){
		func(c *WorkersConfig) { c.RepositoryProviders[1].BaseURL = "http://gitlab.example.com" },
		func(c *WorkersConfig) { c.RepositoryProviders[1].BaseURL = "https://user:password@gitlab.example.com" },
		func(c *WorkersConfig) { c.RepositoryProviders[1].ID = "github" },
		func(c *WorkersConfig) { c.RepositoryProviders[1].AllowedRepositories = nil },
		func(c *WorkersConfig) { c.RepositoryProviders[1].Priority = -1 },
	} {
		c := &WorkersConfig{RepositoryProviders: repositoryTestProviders()}
		change(c)
		if err := c.validateRepositoryProviders(); err == nil {
			t.Fatal("invalid repository configuration accepted")
		}
	}
}

func TestRepositoryBindingSnapshotAndBuildRetry(t *testing.T) {
	s, store, base := workerTestServer(t)
	s.Config.Workers.RepositoryProviders = repositoryTestProviders()
	payload := `{"source":{"provider_id":"company","repository":"https://gitlab.example.com/team/nested/worker.git","ref":"main","root_directory":"worker","entrypoint":"index.ts"},"expected_revision":0}`
	if r := request(s, "PUT", base+"/repository", "krun_cli_viewer", payload); r.Code != 403 {
		t.Fatal(r.Code, r.Body.String())
	}
	if r := request(s, "PUT", base+"/repository", "krun_cli_admin", payload); r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	if r := request(s, "PUT", base+"/repository", "krun_cli_admin", payload); r.Code != 409 {
		t.Fatal(r.Code, r.Body.String())
	}
	buildPayload := `{"source_type":"repository","repository_revision":1,"environment":"fix-demo","idempotency_key":"repository-one"}`
	r := request(s, "POST", base+"/builds", "krun_cli_admin", buildPayload)
	var first WorkerBuild
	if err := json.Unmarshal(r.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	if r.Code != 202 || first.Repository == nil || first.Repository.Repository != "team/nested/worker" || first.SourceDigest != "" {
		t.Fatal(r.Code, r.Body.String())
	}
	updated := strings.Replace(strings.Replace(payload, `"ref":"main"`, `"ref":"release/v2"`, 1), `"expected_revision":0`, `"expected_revision":1`, 1)
	if r := request(s, "PUT", base+"/repository", "krun_cli_admin", updated); r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	retry := request(s, "POST", base+"/builds", "krun_cli_admin", buildPayload)
	var same WorkerBuild
	_ = json.Unmarshal(retry.Body.Bytes(), &same)
	if retry.Code != 200 || same.ID != first.ID || same.Repository.Ref != "main" {
		t.Fatal(retry.Code, retry.Body.String())
	}
	stale := strings.Replace(buildPayload, "repository-one", "repository-two", 1)
	if r := request(s, "POST", base+"/builds", "krun_cli_admin", stale); r.Code != 409 {
		t.Fatal("stale repository configuration accepted", r.Code, r.Body.String())
	}
	if err := store.Update(context.Background(), func(st *State) error {
		if len(st.Workers.BuildPrivate[first.ID].Files) != 0 {
			t.Error("repository source was not queued separately")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryInputBoundaries(t *testing.T) {
	s, _, base := workerTestServer(t)
	s.Config.Workers.RepositoryProviders = repositoryTestProviders()
	for _, source := range []WorkerRepositorySource{
		{ProviderID: "disabled", Repository: "team/worker", Entrypoint: "index.ts"},
		{ProviderID: "company", Repository: "https://attacker.test/team/worker", Entrypoint: "index.ts"},
		{ProviderID: "company", Repository: "other/worker", Entrypoint: "index.ts"},
		{ProviderID: "company", Repository: "team/nested/worker", RootDirectory: "../private", Entrypoint: "index.ts"},
		{ProviderID: "company", Repository: "team/nested/worker", Entrypoint: "../index.ts"},
		{ProviderID: "company", Repository: "team/nested/worker", Ref: "main?token=secret", Entrypoint: "index.ts"},
	} {
		payload, _ := json.Marshal(map[string]any{"source": source, "expected_revision": 0})
		r := request(s, "PUT", base+"/repository", "krun_cli_admin", string(payload))
		if r.Code != 422 && r.Code != 403 {
			t.Fatal(r.Code, r.Body.String())
		}
	}
}

func repositoryTestArchive(t *testing.T, names map[string]string) []byte {
	t.Helper()
	var raw bytes.Buffer
	zip := gzip.NewWriter(&raw)
	archive := tar.NewWriter(zip)
	for name, code := range names {
		if err := archive.WriteHeader(&tar.Header{Name: name, Size: int64(len(code)), Mode: 0644}); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(archive, code); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zip.Close(); err != nil {
		t.Fatal(err)
	}
	return raw.Bytes()
}

func TestGitLabRepositoryCommitAndArchive(t *testing.T) {
	s, _, _ := workerTestServer(t)
	commit := strings.Repeat("a", 40)
	raw := repositoryTestArchive(t, map[string]string{"repo-commit/worker/index.ts": `export default {fetch(){return new Response('gitlab')}}`, "repo-commit/README.md": "ignored"})
	var requests []string
	gitlab := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.RequestURI)
		if r.Header.Get("PRIVATE-TOKEN") != "private-test-token" || r.Header.Get("Authorization") != "" {
			t.Error("wrong GitLab credentials")
		}
		switch r.RequestURI {
		case "/api/v4/projects/team%2Fnested%2Fworker/repository/commits/release%2Fv1":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": commit})
		case "/api/v4/projects/team%2Fnested%2Fworker/repository/archive.tar.gz?sha=" + commit:
			_, _ = w.Write(raw)
		default:
			t.Errorf("wrong GitLab request: %s", r.RequestURI)
			w.WriteHeader(404)
		}
	}))
	defer gitlab.Close()
	s.workerRepositoryClient.Transport = gitlab.Client().Transport
	provider := WorkerRepositoryProvider{Type: "gitlab", BaseURL: gitlab.URL, Token: "private-test-token"}
	source := WorkerRepositorySource{Repository: "team/nested/worker", Ref: "release/v1", RootDirectory: "worker", Entrypoint: "index.ts"}
	resolved, err := s.resolveRepositoryCommit(context.Background(), provider, source)
	if err != nil || resolved != commit {
		t.Fatal(resolved, err)
	}
	files, err := s.fetchRepositorySource(context.Background(), provider, WorkerBuildRepository{source, resolved})
	if err != nil || len(files) != 1 || !strings.Contains(files["index.ts"], "gitlab") || len(requests) != 2 {
		t.Fatal(files, err, requests)
	}
}

func TestGitHubRepositoryRedirectCredentialIsolation(t *testing.T) {
	s, _, _ := workerTestServer(t)
	commit := strings.Repeat("b", 40)
	raw := repositoryTestArchive(t, map[string]string{"repo-commit/index.js": `export default {fetch(){return new Response('github')}}`})
	var downloads int
	s.workerRepositoryClient.Transport = invokeRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		response := &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"sha":"` + commit + `"}`))}
		if r.URL.Host == "api.github.com" {
			if r.Header.Get("Authorization") != "Bearer github-test-token" {
				t.Error("missing GitHub API credentials")
			}
			if strings.Contains(r.URL.Path, "/tarball/") {
				response.StatusCode = 302
				response.Header.Set("Location", "https://codeload.github.com/team/worker/legacy.tar.gz/"+commit)
			}
		} else {
			if r.URL.Host != "codeload.github.com" || r.Header.Get("Authorization") != "" || r.Header.Get("PRIVATE-TOKEN") != "" {
				t.Error("credentials escaped GitHub API origin")
			}
			downloads++
			response.Body = io.NopCloser(bytes.NewReader(raw))
		}
		return response, nil
	})
	provider := WorkerRepositoryProvider{Type: "github", BaseURL: "https://github.com", Token: "github-test-token"}
	source := WorkerRepositorySource{Repository: "team/worker", Ref: "main", RootDirectory: ".", Entrypoint: "index.js"}
	resolved, err := s.resolveRepositoryCommit(context.Background(), provider, source)
	if err != nil {
		t.Fatal(err)
	}
	files, err := s.fetchRepositorySource(context.Background(), provider, WorkerBuildRepository{source, resolved})
	if err != nil || len(files) != 1 || downloads != 1 {
		t.Fatal(files, err, downloads)
	}
	s.workerRepositoryClient.Transport = invokeRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"https://attacker.test/archive"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	if _, err := s.fetchRepositorySource(context.Background(), provider, WorkerBuildRepository{source, resolved}); err == nil {
		t.Fatal("external archive redirect accepted")
	}
}

func TestRepositoryArchiveBoundaries(t *testing.T) {
	source := WorkerBuildRepository{WorkerRepositorySource: WorkerRepositorySource{RootDirectory: "worker", Entrypoint: "index.ts"}}
	for _, files := range []map[string]string{
		{"repo/../worker/index.ts": "unsafe"},
		{"repo/worker/../index.ts": "unsafe"},
		{"repo/worker/index.ts": strings.Repeat("x", (256<<10)+1)},
		{"repo/worker/not-entry.ts": "missing entry"},
		{"repo/worker/index.ts": string([]byte{0xff})},
	} {
		if _, err := extractRepositorySource(repositoryTestArchive(t, files), source); err == nil {
			t.Fatal("invalid archive accepted")
		}
	}
	var buf bytes.Buffer
	zip := gzip.NewWriter(&buf)
	archive := tar.NewWriter(zip)
	_ = archive.WriteHeader(&tar.Header{Name: "repo/worker/index.ts", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"})
	_ = archive.Close()
	_ = zip.Close()
	if _, err := extractRepositorySource(buf.Bytes(), source); err == nil {
		t.Fatal("symlink accepted")
	}
}

func TestRepositoryCommitRemainsPinnedAfterInterruptedDownload(t *testing.T) {
	s, store, base := workerTestServer(t)
	s.Config.Workers.RepositoryProviders = repositoryTestProviders()
	binding := `{"source":{"provider_id":"github","repository":"team/worker","ref":"main","root_directory":".","entrypoint":"index.ts"},"expected_revision":0}`
	if r := request(s, "PUT", base+"/repository", "krun_cli_admin", binding); r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	if r0 := request(s, "POST", base+"/environments", "krun_cli_admin", `{"name":"pin-preview"}`); r0.Code != 201 {
		t.Fatal(r0.Code, r0.Body.String())
	}
	r := request(s, "POST", base+"/builds", "krun_cli_admin", `{"source_type":"repository","repository_revision":1,"environment":"pin-preview","idempotency_key":"pin-one"}`)
	var build WorkerBuild
	_ = json.Unmarshal(r.Body.Bytes(), &build)
	commit := strings.Repeat("a", 40)
	archive := repositoryTestArchive(t, map[string]string{"repo/index.ts": `export default {fetch(){return new Response('pinned')}}`})
	resolutions := 0
	downloads := 0
	s.workerRepositoryClient.Transport = invokeRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "/commits/") {
			resolutions++
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"sha":"` + commit + `"}`))}, nil
		}
		if !strings.HasSuffix(r.URL.Path, "/tarball/"+commit) {
			t.Errorf("download did not use pinned commit: %s", r.URL.Path)
		}
		downloads++
		if downloads == 1 {
			return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader("unavailable"))}, nil
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(archive))}, nil
	})
	var private WorkerBuildPrivate
	_ = store.Update(context.Background(), func(st *State) error { private = st.Workers.BuildPrivate[build.ID]; return nil })
	controller := &workersController{server: s}
	if _, _, err := controller.prepareRepositoryBuild(context.Background(), build, private); err == nil {
		t.Fatal("failed archive download was accepted")
	}
	_ = store.Update(context.Background(), func(st *State) error { build = st.Workers.Builds[build.ID]; return nil })
	if build.Repository.Commit != commit {
		t.Fatal("commit was not persisted before archive download")
	}
	result, input, err := controller.prepareRepositoryBuild(context.Background(), build, private)
	if err != nil || resolutions != 1 || downloads != 2 || result.SourceDigest == "" || len(input.Files) != 1 {
		t.Fatal(result, err, resolutions, downloads)
	}
	if strings.Contains(input.Log, "private-test-token") {
		t.Fatal("repository token leaked into build logs")
	}
}

func TestWorkerServiceListFiltersEnvironmentPermissions(t *testing.T) {
	s, _, base := workerTestServer(t)
	s.Config.Workers.RepositoryProviders = repositoryTestProviders()
	if r := request(s, "PUT", base+"/repository", "krun_cli_admin", `{"source":{"provider_id":"github","repository":"team/worker","entrypoint":"index.ts"},"expected_revision":0}`); r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	payload := `{"name":"test list","expires_in":3600,"policy":{"schema_version":1,"statements":[{"effect":"allow","actions":["service.list"],"resource":{"kind":"organization","organization_id":"org-a","ids":["org-a"]},"conditions":{"environments":["global"]}},{"effect":"allow","actions":["service.read"],"resource":{"kind":"service","organization_id":"org-a","ids":["svc-a"]},"conditions":{"environments":["fix-demo"]}}]}}`
	r := request(s, "POST", "/api/v1/orgs/org-a/api-keys", "krun_cli_admin", payload)
	if r.Code != 201 {
		t.Fatal(r.Code, r.Body.String())
	}
	var key struct{ Key string }
	_ = json.Unmarshal(r.Body.Bytes(), &key)
	r = request(s, "GET", "/api/v1/orgs/org-a/workers/services", key.Key, "")
	var list struct {
		Items []workerServiceSummary `json:"items"`
	}
	_ = json.Unmarshal(r.Body.Bytes(), &list)
	if r.Code != 200 || len(list.Items) != 1 || list.Items[0].ID != "svc-a" || len(list.Items[0].Environments) != 1 || list.Items[0].Environments[0].Name != "fix-demo" {
		t.Fatal(r.Code, r.Body.String())
	}
}

func TestRepositoryArchiveGitHubGlobalPAXHeader(t *testing.T) {
	var buf bytes.Buffer
	zip := gzip.NewWriter(&buf)
	archive := tar.NewWriter(zip)
	if err := archive.WriteHeader(&tar.Header{Name: "pax_global_header", Typeflag: tar.TypeXGlobalHeader, Format: tar.FormatPAX, PAXRecords: map[string]string{"comment": strings.Repeat("a", 40)}}); err != nil {
		t.Fatal(err)
	}
	code := `export default {fetch(){return new Response('github')}}`
	if err := archive.WriteHeader(&tar.Header{Name: "cloudflare-commit/src/index.ts", Size: int64(len(code)), Mode: 0644}); err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(archive, code)
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zip.Close(); err != nil {
		t.Fatal(err)
	}
	files, err := extractRepositorySource(buf.Bytes(), WorkerBuildRepository{WorkerRepositorySource: WorkerRepositorySource{RootDirectory: "src", Entrypoint: "index.ts"}})
	if err != nil || files["index.ts"] != code {
		t.Fatal(files, err)
	}
}
