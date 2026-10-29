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
	"net/url"
	"strings"
	"testing"
	"time"
)

func repositoryTestProviders() []WorkerRepositoryProvider {
	return []WorkerRepositoryProvider{
		{ID: "github", Type: "github", Name: "GitHub", Enabled: true, Priority: 20, BaseURL: "https://github.com"},
		{ID: "company", Type: "gitlab", Name: "Company GitLab", Enabled: true, Priority: 10, BaseURL: "https://gitlab.example.com", AllowPrivateNetwork: true, Token: "private-test-token", AllowedRepositories: []string{"team/nested/worker"}},
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
		func(c *WorkersConfig) { c.RepositoryProviders[0].AllowPrivateNetwork = true },
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
	payload := `{"source":{"provider_id":"company","repository":"https://gitlab.example.com/team/nested/worker.git","ref":"main","root_directory":"worker"},"expected_revision":0}`
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
	if err := store.Update(context.Background(), PartitionAll, func(st *State) error {
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
		{ProviderID: "disabled", Repository: "team/worker"},
		{ProviderID: "company", Repository: "https://attacker.test/team/worker"},
		{ProviderID: "company", Repository: "other/worker"},
		{ProviderID: "company", Repository: "team/nested/worker", RootDirectory: "../private"},
		{ProviderID: "company", Repository: "team/nested/worker", Ref: "main?token=secret"},
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
	source := WorkerBuildRepository{WorkerRepositorySource: WorkerRepositorySource{RootDirectory: "worker"}}
	entrypoint := "index.ts"
	for _, files := range []map[string]string{
		{"repo/../worker/index.ts": "unsafe"},
		{"repo/worker/../index.ts": "unsafe"},
		{"repo/worker/index.ts": strings.Repeat("x", workerRepositoryProjectFileMax+1)},
		{"repo/worker/not-entry.ts": "missing entry"},
		{"repo/worker/index.ts": string([]byte{0xff})},
	} {
		if _, _, err := extractRepositorySource(repositoryTestArchive(t, files), source.RootDirectory, entrypoint); err == nil {
			t.Fatal("invalid archive accepted")
		}
	}
	var buf bytes.Buffer
	zip := gzip.NewWriter(&buf)
	archive := tar.NewWriter(zip)
	_ = archive.WriteHeader(&tar.Header{Name: "repo/worker/index.ts", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"})
	_ = archive.Close()
	_ = zip.Close()
	if _, _, err := extractRepositorySource(buf.Bytes(), source.RootDirectory, entrypoint); err == nil {
		t.Fatal("symlink accepted")
	}
}

func TestRepositoryProjectKeepsWranglerInputsAndRejectsLocalSecrets(t *testing.T) {
	wasm := string([]byte{'\x00', 'a', 's', 'm', 1, 0, 0, 0})
	archive := repositoryTestArchive(t, map[string]string{
		"repo/worker/wrangler.jsonc":            `{"main":"src/index.ts","rules":[{"type":"CompiledWasm","globs":["**/*.wasm"]}]}`,
		"repo/worker/package.json":              `{"private":true}`,
		"repo/worker/package-lock.json":         `{"lockfileVersion":3,"packages":{}}`,
		"repo/worker/.yarnrc.yml":               "nodeLinker: node-modules",
		"repo/worker/src/index.ts":              `import parser from "./parser.wasm"; export default {fetch(){return new Response(String(parser))}}`,
		"repo/worker/src/message.html":          "<p>hello</p>",
		"repo/worker/src/parser.wasm":           wasm,
		"repo/worker/node_modules/pkg/index.js": "must not cross the build boundary",
		"repo/worker/.wrangler/tmp/output.js":   "stale generated output",
	})
	files, _, err := extractRepositorySource(archive, "worker", "src/index.ts")
	if err != nil {
		t.Fatal(err)
	}
	if files["src/parser.wasm"] != wasm || files["src/message.html"] != "<p>hello</p>" || files["package-lock.json"] == "" || files[".yarnrc.yml"] == "" {
		t.Fatalf("complete Wrangler inputs were not retained: %#v", files)
	}
	if _, ok := files["node_modules/pkg/index.js"]; ok {
		t.Fatal("node_modules crossed the repository build boundary")
	}
	if _, ok := files[".wrangler/tmp/output.js"]; ok {
		t.Fatal("stale .wrangler output crossed the repository build boundary")
	}
	bundle, err := workerSourceBundle(files, nil)
	if err != nil {
		t.Fatal(err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(bundle))
	if err != nil {
		t.Fatal(err)
	}
	tarReader := tar.NewReader(gz)
	foundWasm := false
	for {
		header, nextErr := tarReader.Next()
		if nextErr == io.EOF {
			break
		}
		if nextErr != nil {
			t.Fatal(nextErr)
		}
		data, readErr := io.ReadAll(tarReader)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if header.Name == "src/src/parser.wasm" {
			foundWasm = string(data) == wasm
		}
	}
	_ = gz.Close()
	if !foundWasm {
		t.Fatal("binary module did not survive the source bundle round trip")
	}

	for _, secret := range []string{".env", ".env.production", ".dev.vars", ".npmrc", ".yarnrc"} {
		files := map[string]string{
			"repo/worker/wrangler.jsonc": `{"main":"index.ts"}`,
			"repo/worker/index.ts":       "export default {}",
			"repo/worker/" + secret:      "credential=should-not-enter-builder",
		}
		if _, _, err := extractRepositorySource(repositoryTestArchive(t, files), "worker", "index.ts"); err == nil {
			t.Fatalf("sensitive build input %s was accepted", secret)
		}
	}
	authYarn := map[string]string{
		"repo/worker/wrangler.jsonc": `{"main":"index.ts"}`,
		"repo/worker/index.ts":       "export default {}",
		"repo/worker/.yarnrc.yml":    "npmAuthToken: secret-token",
	}
	if _, _, err := extractRepositorySource(repositoryTestArchive(t, authYarn), "worker", "index.ts"); err == nil {
		t.Fatal("package-manager credentials were accepted")
	}
}

func TestWorkerServiceListFiltersEnvironmentPermissions(t *testing.T) {
	s, _, base := workerTestServer(t)
	s.Config.Workers.RepositoryProviders = repositoryTestProviders()
	if r := request(s, "PUT", base+"/repository", "krun_cli_admin", `{"source":{"provider_id":"github","repository":"team/worker"},"expected_revision":0}`); r.Code != 200 {
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
	if r := request(s, "POST", "/api/v1/orgs/org-b/workers/repository/github/connect", "krun_cli_viewer", ""); r.Code != 403 {
		t.Fatal("cross-organization repository connection allowed", r.Code, r.Body.String())
	}
	if r := request(s, "DELETE", "/api/v1/orgs/org-a/workers/repository/github/disconnect", "krun_cli_admin", ""); r.Code != 204 {
		t.Fatal(r.Code, r.Body.String())
	}
	// 手工放置连接后 providers 列表应标记 connected
	if e := store.Update(context.Background(), PartitionAll, func(st *State) error {
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

func TestRepositoryOAuthGETCallbackStoresConnectionAndReturnsToWorkspace(t *testing.T) {
	oauth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/token" || r.Method != http.MethodPost {
			t.Fatalf("unexpected OAuth request %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"user-token","token_type":"bearer","refresh_token":"refresh-token","expires_in":7200}`)
	}))
	defer oauth.Close()

	s, store, _ := workerTestServer(t)
	s.Config.Workers.RepositoryProviders = []WorkerRepositoryProvider{{
		ID: "gitlab", Type: "gitlab", Name: "GitLab", Enabled: true,
		BaseURL: oauth.URL, ClientID: "client", ClientSecret: "secret",
		OrganizationIDs: []string{"org-a"},
	}}
	s.routes()
	browserToken := "repository-browser"
	state := "repository-state"
	if err := store.upsertRepositoryConnectState(context.Background(), state, WorkerRepositoryConnectState{
		User: "admin", Provider: "gitlab", Organization: "org-a",
		Browser: digest(browserToken), Expires: now().Add(time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/workers/repository/gitlab/callback?"+url.Values{
		"state": {state}, "code": {"approved"},
	}.Encode(), nil)
	req.AddCookie(&http.Cookie{Name: s.cookieName("browser"), Value: browserToken})
	res := httptest.NewRecorder()
	s.ServeHTTP(res, req)
	if res.Code != http.StatusSeeOther {
		t.Fatalf("callback status=%d body=%s", res.Code, res.Body.String())
	}
	location, err := url.Parse(res.Header().Get("Location"))
	if err != nil || location.Query().Get("page") != "workers" || location.Query().Get("workspace") != "org-a" || location.Query().Get("repository_status") != "connected" {
		t.Fatalf("callback location=%q err=%v", res.Header().Get("Location"), err)
	}
	connection, ok, err := store.repositoryConnection(context.Background(), "admin", "gitlab")
	if err != nil || !ok || connection.AccessToken != "user-token" || connection.RefreshToken != "refresh-token" || connection.TokenType != "bearer" || connection.ExpiresAt.IsZero() {
		t.Fatalf("connection=%+v ok=%v err=%v", connection, ok, err)
	}

	// OAuth state is one-shot even when a browser replays the exact callback.
	replay := httptest.NewRecorder()
	s.ServeHTTP(replay, req.Clone(context.Background()))
	if replay.Code != http.StatusSeeOther {
		t.Fatalf("replay status=%d", replay.Code)
	}
	replayLocation, _ := url.Parse(replay.Header().Get("Location"))
	if replayLocation.Query().Get("repository_status") != "error" || replayLocation.Query().Get("error") != "WORKER_REPOSITORY_STATE_INVALID" {
		t.Fatalf("replay location=%q", replay.Header().Get("Location"))
	}
}

func TestRepositoryOAuthCallbackRejectsTokenEndpointRedirect(t *testing.T) {
	leaked := 0
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		leaked++
	}))
	defer target.Close()
	tokenEndpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/capture", http.StatusTemporaryRedirect)
	}))
	defer tokenEndpoint.Close()

	s, store, _ := workerTestServer(t)
	s.Config.Workers.RepositoryProviders = []WorkerRepositoryProvider{{
		ID: "gitlab", Type: "gitlab", Name: "GitLab", Enabled: true,
		BaseURL: tokenEndpoint.URL, ClientID: "client", ClientSecret: "private-client-secret",
		OrganizationIDs: []string{"org-a"},
	}}
	s.routes()
	browserToken := "redirect-browser"
	state := "redirect-state"
	if err := store.upsertRepositoryConnectState(context.Background(), state, WorkerRepositoryConnectState{
		User: "admin", Provider: "gitlab", Organization: "org-a",
		Browser: digest(browserToken), Expires: now().Add(time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/workers/repository/gitlab/callback?"+url.Values{
		"state": {state}, "code": {"approved"},
	}.Encode(), nil)
	req.AddCookie(&http.Cookie{Name: s.cookieName("browser"), Value: browserToken})
	res := httptest.NewRecorder()
	s.ServeHTTP(res, req)
	if res.Code != http.StatusSeeOther || leaked != 0 {
		t.Fatalf("callback status=%d redirected credential requests=%d", res.Code, leaked)
	}
	location, _ := url.Parse(res.Header().Get("Location"))
	if location.Query().Get("repository_status") != "error" {
		t.Fatalf("unexpected callback location %q", res.Header().Get("Location"))
	}
	if _, ok, err := store.repositoryConnection(context.Background(), "admin", "gitlab"); err != nil || ok {
		t.Fatalf("redirected OAuth callback stored a connection: ok=%v err=%v", ok, err)
	}
}

func TestRepositoryOAuthTransportRejectsPrivateProductionAddressAndOversizeBody(t *testing.T) {
	t.Run("production private address", func(t *testing.T) {
		hits := 0
		endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits++
			_, _ = io.WriteString(w, `{"access_token":"must-not-be-read"}`)
		}))
		defer endpoint.Close()
		s := &Server{Config: Config{Development: false, PublicURL: "https://console.example.test"}}
		provider := WorkerRepositoryProvider{ID: "gitlab", Type: "gitlab", BaseURL: strings.Replace(endpoint.URL, "http://", "https://", 1), ClientID: "client", ClientSecret: "secret"}
		ctx, cancel, err := s.repositoryOAuthContext(context.Background(), provider)
		if err != nil {
			t.Fatal(err)
		}
		defer cancel()
		deadline, bounded := ctx.Deadline()
		if remaining := time.Until(deadline); !bounded || remaining > 15*time.Second || remaining <= 0 {
			t.Fatal("repository OAuth exchange context is not bounded")
		}
		_, err = s.repositoryOAuthConfig(provider).Exchange(ctx, "code")
		if err == nil || hits != 0 {
			t.Fatalf("private production endpoint reached=%d err=%v", hits, err)
		}
	})

	t.Run("bounded response", func(t *testing.T) {
		endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"access_token":"token","padding":"`+strings.Repeat("x", repositoryOAuthResponseLimitBytes)+`"}`)
		}))
		defer endpoint.Close()
		s := &Server{Config: Config{Development: true, PublicURL: "http://127.0.0.1:8080"}}
		provider := WorkerRepositoryProvider{ID: "gitlab", Type: "gitlab", BaseURL: endpoint.URL, ClientID: "client", ClientSecret: "secret"}
		ctx, cancel, err := s.repositoryOAuthContext(context.Background(), provider)
		if err != nil {
			t.Fatal(err)
		}
		defer cancel()
		_, err = s.repositoryOAuthConfig(provider).Exchange(ctx, "code")
		if err == nil || !strings.Contains(err.Error(), "exceeds limit") {
			t.Fatalf("oversize token response was not rejected: %v", err)
		}
	})
}

func TestRepositoryCredentialRefreshRotatesGrantWithoutSharedPATFallback(t *testing.T) {
	var refreshes int
	oauth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/token" || r.Method != http.MethodPost {
			t.Fatalf("unexpected refresh request %s %s", r.Method, r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != "old-refresh" {
			t.Fatal("refresh grant was not sent")
		}
		refreshes++
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"rotated-access","token_type":"bearer","refresh_token":"rotated-refresh","expires_in":7200}`)
	}))
	defer oauth.Close()

	s, store, _ := workerTestServer(t)
	provider := WorkerRepositoryProvider{
		ID: "gitlab", Type: "gitlab", Name: "GitLab", Enabled: true,
		BaseURL: oauth.URL, ClientID: "client", ClientSecret: "secret",
		Token: "shared-pat", AllowedRepositories: []string{"team/repository"},
	}
	if err := store.Update(context.Background(), PartitionWorkers, func(*State) error {
		return store.upsertRepositoryConnection(context.Background(), WorkerRepositoryConnection{
			User: "admin", Provider: "gitlab", AccessToken: "expired-access",
			TokenType: "bearer", RefreshToken: "old-refresh",
			ExpiresAt: now().Add(-time.Minute), ConnectedAt: now(),
		})
	}); err != nil {
		t.Fatal(err)
	}
	token, err := s.repositoryCredential(context.Background(), provider, "admin")
	if err != nil || token != "rotated-access" || token == provider.Token || refreshes != 1 {
		t.Fatalf("token=%q refreshes=%d err=%v", token, refreshes, err)
	}
	var connection WorkerRepositoryConnection
	var ok bool
	if err = store.View(context.Background(), PartitionWorkers, func(*State) error {
		connection, ok, err = store.repositoryConnection(context.Background(), "admin", "gitlab")
		return err
	}); err != nil || !ok {
		t.Fatal(err)
	}
	if connection.AccessToken != "rotated-access" || connection.RefreshToken != "rotated-refresh" || connection.TokenRevision != 3 || connection.RefreshState != "ready" {
		t.Fatalf("rotated connection metadata is incomplete: %+v", connection)
	}
	raw, err := json.Marshal(connection)
	if err != nil || bytes.Contains(raw, []byte("rotated-refresh")) || bytes.Contains(raw, []byte("rotated-access")) {
		t.Fatal("repository grant leaked through JSON")
	}
}

func TestRepositoryCredentialRejectedRefreshRequiresReconnect(t *testing.T) {
	oauth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"invalid_grant"}`)
	}))
	defer oauth.Close()
	s, store, _ := workerTestServer(t)
	provider := WorkerRepositoryProvider{
		ID: "gitlab", Type: "gitlab", Name: "GitLab", Enabled: true,
		BaseURL: oauth.URL, ClientID: "client", ClientSecret: "secret",
		Token: "shared-pat", AllowedRepositories: []string{"team/repository"},
	}
	if err := store.Update(context.Background(), PartitionWorkers, func(*State) error {
		return store.upsertRepositoryConnection(context.Background(), WorkerRepositoryConnection{
			User: "admin", Provider: "gitlab", AccessToken: "expired-access",
			RefreshToken: "rejected-refresh", ExpiresAt: now().Add(-time.Minute), ConnectedAt: now(),
		})
	}); err != nil {
		t.Fatal(err)
	}
	token, err := s.repositoryCredential(context.Background(), provider, "admin")
	if err == nil || token != "" || !strings.Contains(err.Error(), "reconnect") {
		t.Fatalf("rejected grant token=%q err=%v", token, err)
	}
	var connected map[string]bool
	if viewErr := store.View(context.Background(), PartitionWorkers, func(*State) error {
		connected, err = store.connectedRepositoryProviders(context.Background(), "admin")
		return err
	}); viewErr != nil {
		t.Fatal(viewErr)
	}
	if connected["gitlab"] {
		t.Fatal("provider still shown as connected after invalid_grant")
	}
}
