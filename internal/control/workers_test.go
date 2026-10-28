package control

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func workerTestServer(t *testing.T) (*Server, *Store, string) {
	t.Helper()
	s, store := testServer(t)
	s.Config.Workers = testWorkersConfig()
	s.routes()
	// A default preview so build/permission tests have a non-baseline target.
	if e := store.Update(context.Background(), PartitionAll, func(st *State) error {
		ws := s.workerService(st, Target{ID: "svc-a", Organization: "org-a"})
		ws.Environments["fix-demo"] = WorkerEnvironment{Name: "fix-demo", Enabled: true, Domains: []string{"internal"}, ConfigRevision: 1, Variables: map[string]string{}}
		st.Workers.Services["svc-a"] = ws
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	return s, store, "/api/v1/orgs/org-a/services/svc-a/workers"
}
func TestWorkerBuildPermissionScopeAndIdempotency(t *testing.T) {
	s, store, base := workerTestServer(t)
	payload := `{"environment":"fix-demo","entrypoint":"index.ts","files":{"index.ts":"export default {fetch(){return new Response('ok')}}"},"idempotency_key":"build-one"}`
	if r := request(s, "POST", base+"/builds", "krun_cli_viewer", payload); r.Code != 403 {
		t.Fatal(r.Code, r.Body.String())
	}
	first := request(s, "POST", base+"/builds", "krun_cli_admin", payload)
	if first.Code != 202 {
		t.Fatal(first.Code, first.Body.String())
	}
	var b WorkerBuild
	_ = json.Unmarshal(first.Body.Bytes(), &b)
	retry := request(s, "POST", base+"/builds", "krun_cli_admin", payload)
	var same WorkerBuild
	_ = json.Unmarshal(retry.Body.Bytes(), &same)
	if retry.Code != 200 || same.ID != b.ID {
		t.Fatal(retry.Code, retry.Body.String())
	}
	changed := strings.Replace(payload, "'ok'", "'different'", 1)
	if r := request(s, "POST", base+"/builds", "krun_cli_admin", changed); r.Code != 409 {
		t.Fatal(r.Code, r.Body.String())
	}
	r := request(s, "GET", base+"/builds/"+b.ID, "krun_cli_admin", "")
	if strings.Contains(r.Body.String(), "fetch()") || strings.Contains(r.Body.String(), "Files") || strings.Contains(r.Body.String(), "Actor") {
		t.Fatal("private source/identity leaked", r.Body.String())
	}
	if r := request(s, "GET", "/api/v1/orgs/org-b/services/svc-b/workers/builds/"+b.ID, "krun_cli_admin", ""); r.Code != 404 {
		t.Fatal(r.Code, r.Body.String())
	}
	_ = store.Update(context.Background(), PartitionAll, func(st *State) error {
		if len(st.Workers.Builds) != 1 || len(st.Workers.BuildPrivate[b.ID].Files) != 1 {
			t.Fatal("queue/source not persisted")
		}
		return nil
	})
}

func TestWorkerPreviewLimitAndRevisionLifecycle(t *testing.T) {
	s, store, base := workerTestServer(t)
	s.Config.Workers.PreviewLimit = 1
	if r := request(s, "POST", base+"/environments", "krun_cli_admin", `{"name":"second"}`); r.Code != 409 {
		t.Fatalf("preview limit not enforced: %d %s", r.Code, r.Body.String())
	}
	if r := request(s, "DELETE", base+"/environments/fix-demo", "krun_cli_admin", ""); r.Code != 204 {
		t.Fatalf("delete preview: %d %s", r.Code, r.Body.String())
	}
	if r := request(s, "POST", base+"/environments", "krun_cli_admin", `{"name":"second"}`); r.Code != 201 {
		t.Fatalf("create replacement preview: %d %s", r.Code, r.Body.String())
	}
	if r := request(s, "POST", base+"/environments", "krun_cli_admin", `{"name":"third"}`); r.Code != 409 {
		t.Fatalf("replacement exceeded preview limit: %d %s", r.Code, r.Body.String())
	}
	if err := store.Update(context.Background(), PartitionWorkers, func(st *State) error {
		service := st.Workers.Services["svc-a"]
		if service.PreviewRevision != 2 || previewCount(service.Environments) != 1 {
			t.Fatalf("preview lifecycle state: %+v", service)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestTerminalBuildRetentionDoesNotBlockNextBuild(t *testing.T) {
	s, store, base := workerTestServer(t)
	if err := store.Update(context.Background(), PartitionWorkers, func(st *State) error {
		for i := 0; i < workerBuildRetainPerService; i++ {
			id := fmt.Sprintf("old-%03d", i)
			st.Workers.Builds[id] = WorkerBuild{ID: id, Organization: "org-a", Service: "svc-a", Environment: "production", State: "succeeded", Created: now()}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	payload := `{"environment":"production","entrypoint":"index.ts","files":{"index.ts":"export default {fetch(){return new Response('ok')}}"},"idempotency_key":"after-retention-cap"}`
	if response := request(s, "POST", base+"/builds", "krun_cli_admin", payload); response.Code != 202 {
		t.Fatalf("terminal build history permanently blocked a new build: %d %s", response.Code, response.Body.String())
	}
}

func TestWorkerOverviewAggregatesPollingState(t *testing.T) {
	s, store, base := workerTestServer(t)
	build := seedWorkerBuild(t, s, store, base)
	if err := store.Update(context.Background(), PartitionWorkers, func(st *State) error {
		release := WorkerRelease{ID: "rel-overview", Service: "svc-a", Organization: "org-a", Environment: "production", Artifact: build.Artifact, Pool: "default", Created: now()}
		st.Workers.Releases[release.ID] = release
		service := st.Workers.Services["svc-a"]
		env := service.Environments["production"]
		env.Release, env.Confirmed = release.ID, release.ID
		service.Environments["production"] = env
		st.Workers.Services[service.Service] = service
		st.Workers.Histories["svc-a/production"] = []WorkerHistory{{Revision: 1, Release: release.ID, Operation: "op-overview", Kind: "publish", Time: now()}}
		st.Workers.Operations["op-overview"] = WorkerOperation{ID: "op-overview", Service: "svc-a", Organization: "org-a", Environment: "production", State: "available", Release: release.ID, Created: now()}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	response := request(s, "GET", base+"/overview", "krun_cli_admin", "")
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	var overview struct {
		Builds       []WorkerBuild               `json:"builds"`
		Environments []workerOverviewEnvironment `json:"environments"`
		Operations   []WorkerOperation           `json:"operations"`
		Releases     map[string]WorkerRelease    `json:"releases"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &overview); err != nil {
		t.Fatal(err)
	}
	if len(overview.Builds) != 1 || len(overview.Environments) != 2 || len(overview.Operations) != 1 {
		t.Fatalf("unexpected overview: %+v", overview)
	}
	if overview.Releases["rel-overview"].Artifact != build.Artifact {
		t.Fatalf("history release missing from overview: %+v", overview.Releases)
	}
	if overview.Environments[0].Environment.Name != "production" || len(overview.Environments[0].History) != 1 || overview.Environments[0].CurrentRelease == nil {
		t.Fatalf("production overview incomplete: %+v", overview.Environments[0])
	}
}
func TestWorkerSourcesCannotEscapeOrOverridePlatformEntry(t *testing.T) {
	cases := []struct {
		entry string
		files map[string]string
	}{{"../evil.ts", map[string]string{"../evil.ts": "x"}}, {"/etc/passwd.js", map[string]string{"/etc/passwd.js": "x"}}, {"a/../index.ts", map[string]string{"a/../index.ts": "x"}}, {"__krun_entry__.mjs", map[string]string{"__krun_entry__.mjs": "x"}}, {"index.ts", map[string]string{"index.ts": strings.Repeat("x", 256<<10+1)}}}
	for _, tc := range cases {
		if sourceValid(tc.entry, tc.files) == nil {
			t.Fatal("source accepted", tc.entry)
		}
	}
	if e := sourceValid("src/index.ts", map[string]string{"src/index.ts": "import './util'", "src/util.ts": "export const x=1"}); e != nil {
		t.Fatal(e)
	}
}
func seedWorkerBuild(t *testing.T, s *Server, store *Store, base string) WorkerBuild {
	r := request(s, "POST", base+"/builds", "krun_cli_admin", `{"environment":"production","entrypoint":"index.ts","files":{"index.ts":"export default {fetch(){return new Response('ok')}}"},"idempotency_key":"seed"}`)
	if r.Code != 202 {
		t.Fatal(r.Code, r.Body.String())
	}
	var b WorkerBuild
	_ = json.Unmarshal(r.Body.Bytes(), &b)
	_ = store.Update(context.Background(), PartitionAll, func(st *State) error {
		b.State = "succeeded"
		b.Artifact = "a"
		st.Workers.Builds[b.ID] = b
		st.Workers.Artifacts["a"] = WorkerArtifact{ID: "a", Organization: "org-a", State: "finalized", ArchiveDigest: "archive", ModuleDigest: "module"}
		return nil
	})
	return b
}
func TestWorkerReleaseQueuesImmutableConfigurationAndRejectsInvalidTarget(t *testing.T) {
	s, store, base := workerTestServer(t)
	b := seedWorkerBuild(t, s, store, base)
	publish := `{"build_id":"` + b.ID + `","expected_revision":0,"idempotency_key":"publish-one"}`
	if r := request(s, "POST", base+"/environments/missing-preview/releases", "krun_cli_admin", publish); r.Code != 404 {
		t.Fatal(r.Code, r.Body.String())
	}
	first := request(s, "POST", base+"/environments/production/releases", "krun_cli_admin", publish)
	if first.Code != 202 {
		t.Fatal(first.Code, first.Body.String())
	}
	var op WorkerOperation
	_ = json.Unmarshal(first.Body.Bytes(), &op)
	if r := request(s, "POST", base+"/environments/production/releases", "krun_cli_admin", publish); r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	if r := request(s, "PATCH", base+"/environments/production", "krun_cli_admin", `{"expected_revision":1,"variables":{"VALUE":"new"}}`); r.Code != 409 {
		t.Fatal(r.Code, r.Body.String())
	}
	if r := request(s, "POST", base+"/environments/missing-preview/rollbacks", "krun_cli_admin", `{"release_id":"`+op.Release+`","expected_revision":0,"idempotency_key":"rollback-wrong-environment"}`); r.Code != 404 {
		t.Fatal(r.Code, r.Body.String())
	}
	_ = store.Update(context.Background(), PartitionAll, func(st *State) error {
		env := st.Workers.Services["svc-a"].Environments["production"]
		if env.Revision != 0 || env.Release != "" {
			t.Fatal("enqueue committed a release")
		}
		d := st.Workers.Releases[op.Release]
		if d.Environment != "production" || d.ConfigRevision != 1 {
			t.Fatal(d)
		}
		return nil
	})
}
func TestWorkerActorKeyRevocationIsRechecked(t *testing.T) {
	s, store, base := workerTestServer(t)
	b := seedWorkerBuild(t, s, store, base)
	_ = b
	_ = store.Update(context.Background(), PartitionAll, func(st *State) error {
		user := st.Users["admin"]
		st.Keys["key"] = APIKey{ID: "key", Owner: user.ID, ScopeKind: "system", Policy: Policy{Version: 1, Statements: []Statement{{Effect: "allow", Actions: []string{"build.start"}, Resource: Selector{Kind: "service", Organization: "org-a", IDs: []string{"svc-a"}}}}}}
		a := WorkerActor{User: user.ID, Key: "key", AuthVersion: user.AuthVersion}
		if s.workerAuthorizeActor(st, a, "svc-a", "fix-demo", "build.start") != nil {
			t.Fatal("valid key denied")
		}
		key := st.Keys["key"]
		key.Revoked = true
		st.Keys["key"] = key
		if s.workerAuthorizeActor(st, a, "svc-a", "fix-demo", "build.start") == nil {
			t.Fatal("revoked queued credential accepted")
		}
		return nil
	})
}

func TestWorkerRollbackCanRecoverACommittedRelease(t *testing.T) {
	s, store, base := workerTestServer(t)
	b := seedWorkerBuild(t, s, store, base)
	first := request(s, "POST", base+"/environments/production/releases", "krun_cli_admin", `{"build_id":"`+b.ID+`","expected_revision":0,"idempotency_key":"first"}`)
	var op WorkerOperation
	_ = json.Unmarshal(first.Body.Bytes(), &op)
	_ = store.Update(context.Background(), PartitionAll, func(st *State) error {
		ws := st.Workers.Services["svc-a"]
		env := ws.Environments["production"]
		env.Revision, env.Release, env.Confirmed = 2, op.Release, op.Release
		ws.Environments["production"] = env
		st.Workers.Services["svc-a"] = ws
		st.Workers.Histories["svc-a/production"] = []WorkerHistory{{Revision: 1, Release: op.Release}}
		op.State = "committed"
		st.Workers.Operations[op.ID] = op
		return nil
	})
	if r := request(s, "POST", base+"/environments/production/rollbacks", "krun_cli_admin", `{"release_id":"missing","expected_revision":2,"idempotency_key":"bad"}`); r.Code != 404 {
		t.Fatal(r.Code, r.Body.String())
	}
	if r := request(s, "POST", base+"/environments/production/rollbacks", "krun_cli_admin", `{"release_id":"`+op.Release+`","expected_revision":2,"idempotency_key":"recover"}`); r.Code != 202 {
		t.Fatal(r.Code, r.Body.String())
	}
	_ = store.Update(context.Background(), PartitionAll, func(st *State) error {
		if st.Workers.Operations[op.ID].State != "committed" || st.Workers.Services["svc-a"].Environments["production"].Revision != 2 {
			t.Fatal("rollback enqueue changed the active release before validation")
		}
		return nil
	})
}

// A committed operation is durable even before every runtime Pod has acked.
// It must not block the next publish; the newer commit supersedes convergence.
func TestCommittedReleaseDoesNotBlockNewPublish(t *testing.T) {
	s, store, base := workerTestServer(t)
	b := seedWorkerBuild(t, s, store, base)
	first := request(s, "POST", base+"/environments/production/releases", "krun_cli_admin", `{"build_id":"`+b.ID+`","expected_revision":0,"idempotency_key":"first-committed"}`)
	if first.Code != 202 {
		t.Fatal(first.Code, first.Body.String())
	}
	var op WorkerOperation
	_ = json.Unmarshal(first.Body.Bytes(), &op)
	_ = store.Update(context.Background(), PartitionAll, func(st *State) error {
		ws := st.Workers.Services["svc-a"]
		env := ws.Environments["production"]
		env.Revision, env.Release = 2, op.Release
		ws.Environments["production"] = env
		st.Workers.Services["svc-a"] = ws
		op.State = "committed"
		st.Workers.Operations[op.ID] = op
		return nil
	})
	next := request(s, "POST", base+"/environments/production/releases", "krun_cli_admin", `{"build_id":"`+b.ID+`","expected_revision":2,"idempotency_key":"after-committed"}`)
	if next.Code != 202 {
		t.Fatal(next.Code, next.Body.String())
	}
}

func TestWorkerEnvironmentScopedKeyCanReadCollections(t *testing.T) {
	s, store, base := workerTestServer(t)
	seedWorkerBuild(t, s, store, base)
	r := request(s, "POST", "/api/v1/orgs/org-a/api-keys", "krun_cli_admin", `{"name":"test only","expires_in":3600,"policy":{"schema_version":1,"statements":[{"effect":"allow","actions":["service.read","service.version.read"],"resource":{"kind":"service","organization_id":"org-a","ids":["svc-a"]},"conditions":{"environments":["fix-demo"]}}]}}`)
	if r.Code != 201 {
		t.Fatal(r.Code, r.Body.String())
	}
	var issued struct{ Key string }
	_ = json.Unmarshal(r.Body.Bytes(), &issued)
	envs := request(s, "GET", base+"/environments", issued.Key, "")
	if envs.Code != 200 || strings.Contains(envs.Body.String(), "production") || !strings.Contains(envs.Body.String(), `"fix-demo"`) {
		t.Fatal(envs.Code, envs.Body.String())
	}
	preview := request(s, "POST", base+"/builds", "krun_cli_admin", `{"environment":"fix-demo","entrypoint":"index.ts","files":{"index.ts":"export default {fetch(){return new Response('preview')}}"},"idempotency_key":"scoped-preview"}`)
	if preview.Code != 202 {
		t.Fatal(preview.Code, preview.Body.String())
	}
	builds := request(s, "GET", base+"/builds", issued.Key, "")
	if builds.Code != 200 || !strings.Contains(builds.Body.String(), `"environment":"fix-demo"`) || strings.Contains(builds.Body.String(), `"environment":"production"`) {
		t.Fatal(builds.Code, builds.Body.String())
	}
	if r := request(s, "GET", base+"/artifacts/a", issued.Key, ""); r.Code != 404 {
		t.Fatal(r.Code, r.Body.String())
	}
}
func workerArchive(t *testing.T, names []string, module string, link bool) []byte {
	t.Helper()
	manifest, _ := json.Marshal(workerManifest{Schema: 1, Profile: workerProfile, MainModule: "index.js", Modules: []workerManifestModule{{Name: "index.js", Path: "modules/index.js", Type: "esm", Digest: bytesDigest([]byte(module)), Size: len(module)}}})
	var raw bytes.Buffer
	gz := gzip.NewWriter(&raw)
	tr := tar.NewWriter(gz)
	for _, name := range names {
		data := []byte(module)
		if name == "manifest.json" {
			data = manifest
		}
		h := &tar.Header{Name: name, Size: int64(len(data)), Mode: 0600, Typeflag: tar.TypeReg}
		if link {
			h.Typeflag = tar.TypeSymlink
			h.Linkname = "/etc/passwd"
			h.Size = 0
		}
		if e := tr.WriteHeader(h); e != nil {
			t.Fatal(e)
		}
		if !link {
			_, _ = tr.Write(data)
		}
	}
	_ = tr.Close()
	_ = gz.Close()
	return raw.Bytes()
}
func TestWorkerArchiveIntegrityAndTraversal(t *testing.T) {
	raw := workerArchive(t, []string{"manifest.json", "modules/index.js"}, "export default {}", false)
	code, _, e := parseWorkerArchive(raw)
	if e != nil || code.ArchiveDigest != bytesDigest(raw) {
		t.Fatal(e, code)
	}
	for _, bad := range [][]byte{workerArchive(t, []string{"manifest.json", "../../escape"}, "x", false), workerArchive(t, []string{"manifest.json", "modules/index.js", "modules/index.js"}, "x", false), workerArchive(t, []string{"manifest.json", "modules/index.js"}, "x", true), raw[:len(raw)-4]} {
		if _, _, e := parseWorkerArchive(bad); e == nil {
			t.Fatal("unsafe or truncated archive accepted")
		}
	}
}
func TestWorkerRoutesReportUnconfiguredInsteadOfMissing(t *testing.T) {
	s, _ := testServer(t)
	r := request(s, http.MethodGet, "/api/v1/orgs/org-a/workers/services", "krun_cli_admin", "")
	if r.Code != 503 || !strings.Contains(r.Body.String(), "WORKERS_NOT_CONFIGURED") {
		t.Fatalf("unconfigured workers route: %d %s", r.Code, r.Body.String())
	}
	me := request(s, http.MethodGet, "/api/auth/me", "krun_cli_admin", "")
	if me.Code != 200 || !strings.Contains(me.Body.String(), `"workers_configured":false`) {
		t.Fatalf("me should advertise disabled workers: %d %s", me.Code, me.Body.String())
	}
}

func TestWorkerRuntimeChannelRequiresCredentialAndPool(t *testing.T) {
	s, _, _ := workerTestServer(t)
	token := s.Config.Workers.defaultRuntimePool().RuntimeToken
	r := httptest.NewRequest("GET", "/api/internal/workers/config/snapshot", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	if s.workerRuntimeAllowed(r) {
		t.Fatal("missing pool accepted")
	}
	r.Header.Set("X-Runtime-Pool", "default")
	if !s.workerRuntimeAllowed(r) {
		t.Fatal("valid workload credential denied")
	}
	r.Header.Set("X-Runtime-Pool", "other")
	if s.workerRuntimeAllowed(r) {
		t.Fatal("foreign pool accepted")
	}
	if r := request(s, http.MethodGet, "/api/internal/workers/config/snapshot", "", ""); r.Code != 404 {
		t.Fatal("internal endpoint must not be reachable on the public mux:", r.Code)
	}
	if r := request(s, http.MethodGet, "/internal/workers/config/snapshot", "", ""); r.Code != 404 {
		t.Fatal("root-level internal path must stay unreachable:", r.Code)
	}
}

func TestWorkerReleaseAPIHidesRuntimePool(t *testing.T) {
	s, store, base := workerTestServer(t)
	release := seedPublishedWorker(t, store)
	response := request(s, http.MethodGet, base+"/releases/"+release.ID, "krun_cli_admin", "")
	if response.Code != http.StatusOK {
		t.Fatalf("release: %d %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), `"pool"`) || strings.Contains(response.Body.String(), "runtime-default") {
		t.Fatalf("runtime placement leaked: %s", response.Body.String())
	}
}

func TestWorkerConsoleReadsRespectServiceAndEnvironment(t *testing.T) {
	s, store, base := workerTestServer(t)
	b := seedWorkerBuild(t, s, store, base)
	r := request(s, "POST", base+"/environments/production/releases", "krun_cli_admin", `{"build_id":"`+b.ID+`","expected_revision":0,"idempotency_key":"console"}`)
	var op WorkerOperation
	_ = json.Unmarshal(r.Body.Bytes(), &op)
	if r.Code != 202 {
		t.Fatal(r.Code, r.Body.String())
	}
	if r := request(s, "GET", base+"/releases/"+op.Release, "krun_cli_admin", ""); r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	if r := request(s, "GET", "/api/v1/orgs/org-b/services/svc-b/workers/releases/"+op.Release, "krun_cli_admin", ""); r.Code != 404 {
		t.Fatal(r.Code, r.Body.String())
	}
	r = request(s, "POST", "/api/v1/orgs/org-a/api-keys", "krun_cli_admin", `{"name":"test read","expires_in":3600,"policy":{"schema_version":1,"statements":[{"effect":"allow","actions":["service.read","service.version.read"],"resource":{"kind":"service","organization_id":"org-a","ids":["svc-a"]},"conditions":{"environments":["fix-demo"]}}]}}`)
	var key struct{ Key string }
	_ = json.Unmarshal(r.Body.Bytes(), &key)
	if r := request(s, "GET", base+"/releases/"+op.Release, key.Key, ""); r.Code != 403 {
		t.Fatal(r.Code, r.Body.String())
	}
	if r := request(s, "GET", base+"/operations", key.Key, ""); r.Code != 200 || strings.Contains(r.Body.String(), op.ID) {
		t.Fatal(r.Code, r.Body.String())
	}
}

func TestWorkerConsoleInvocationRejectsUnauthorizedAndUnpublishedTargets(t *testing.T) {
	s, _, base := workerTestServer(t)
	for _, path := range []string{"https://attacker.test/", "//attacker.test/"} {
		payload, _ := json.Marshal(map[string]string{"path": path})
		if r := request(s, "POST", base+"/environments/fix-demo/invoke", "krun_cli_admin", string(payload)); r.Code != 422 {
			t.Fatal(r.Code, r.Body.String())
		}
	}
	if r := request(s, "POST", base+"/environments/fix-demo/invoke", "krun_cli_viewer", `{"path":"/"}`); r.Code != 403 {
		t.Fatal(r.Code, r.Body.String())
	}
	if r := request(s, "POST", base+"/environments/fix-demo/invoke", "krun_cli_admin", `{"path":"/"}`); r.Code != 409 {
		t.Fatal(r.Code, r.Body.String())
	}
}

// testWorkersConfig is a minimal catalog: one Gateway, one default domain.
func testWorkersConfig() *WorkersConfig {
	return &WorkersConfig{
		Namespace:   "workers-mvp",
		BuilderMode: "local",

		Gateways: []WorkerGateway{
			{ID: "internal", Name: "workers-mvp", Namespace: "kgateway-system", Section: "private"},
		},
		Domains: []WorkerDomain{
			{ID: "internal", Domain: "workers.test", Label: "内网", Access: "private", Gateway: "internal", Default: true},
		},
	}
}
