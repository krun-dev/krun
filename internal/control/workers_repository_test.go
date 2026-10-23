package control

import (
	"bytes"
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
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
		Items []repositoryConnectionInfo `json:"items"`
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

func TestRepositoryArchiveBoundaries(t *testing.T) {
	source := WorkerBuildRepository{WorkerRepositorySource: WorkerRepositorySource{RootDirectory: "worker", Entrypoint: "index.ts"}}
	for _, files := range []map[string]string{
		{"repo/../worker/index.ts": "unsafe"},
		{"repo/worker/../index.ts": "unsafe"},
		{"repo/worker/index.ts": strings.Repeat("x", (256<<10)+1)},
		{"repo/worker/not-entry.ts": "missing entry"},
		{"repo/worker/index.ts": string([]byte{0xff})},
	} {
		if _, _, err := extractRepositorySource(repositoryTestArchive(t, files), source.RootDirectory, source.Entrypoint); err == nil {
			t.Fatal("invalid archive accepted")
		}
	}
	var buf bytes.Buffer
	zip := gzip.NewWriter(&buf)
	archive := tar.NewWriter(zip)
	_ = archive.WriteHeader(&tar.Header{Name: "repo/worker/index.ts", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"})
	_ = archive.Close()
	_ = zip.Close()
	if _, _, err := extractRepositorySource(buf.Bytes(), source.RootDirectory, source.Entrypoint); err == nil {
		t.Fatal("symlink accepted")
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
	files, _, err := extractRepositorySource(buf.Bytes(), "src", "index.ts")
	if err != nil || files["index.ts"] != code {
		t.Fatal(files, err)
	}
}

func TestRepositoryConnectionLifecycle(t *testing.T) {
	s, store, _ := workerTestServer(t)
	s.Config.Workers.RepositoryProviders = []WorkerRepositoryProvider{
		{ID: "github", Type: "github", Name: "GitHub", Enabled: true, Priority: 10, ClientID: "cid", ClientSecret: "csecret"},
	}
	s.routes()
	if r := request(s, "POST", "/api/v1/orgs/org-a/workers/repository/github/disconnect", "krun_cli_admin", ""); r.Code != 204 {
		t.Fatal(r.Code, r.Body.String())
	}
	// 手工放置连接后 providers 列表应标记 connected
	if e := store.Update(context.Background(), func(st *State) error {
		st.RepositoryConnections["admin\x00github"] = WorkerRepositoryConnection{User: "admin", Provider: "github", AccessToken: "tok"}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	r := request(s, "GET", "/api/v1/orgs/org-a/workers/repository-providers", "krun_cli_admin", "")
	var out struct {
		Items []repositoryConnectionInfo `json:"items"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &out); err != nil || r.Code != 200 || len(out.Items) != 1 {
		t.Fatal(r.Code, r.Body.String())
	}
	if !out.Items[0].Connected || !out.Items[0].OAuth {
		t.Fatal("connection state not surfaced", out.Items)
	}
	if strings.Contains(r.Body.String(), "tok") {
		t.Fatal("access token leaked in provider listing")
	}
}
