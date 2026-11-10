package control

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	kruncli "github.com/krun-dev/krun/internal/cli"
	"github.com/krun-dev/krun/internal/domain"
)

func TestCLIWorkerWorkflow(t *testing.T) {
	s, store, _ := workerTestServer(t)
	enableWorkerSecrets(s.Config.Workers)
	s.Config.Workers.RepositoryProviders = repositoryTestProviders()
	s.Config.Workers.RepositoryProviders[0].ClientID = "fixture-client"
	if err := store.Update(t.Context(), PartitionAll, func(st *State) error {
		org := st.Organizations["org-a"]
		org.ShortName = "cli-test"
		st.Organizations[org.ID] = org
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertRepositoryConnection(t.Context(), domain.WorkerRepositoryConnection{
		User: "admin", Provider: "github", AccessToken: "fixture-git-grant",
	}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The test replaces the asynchronous builder with a finalized artifact.
		if path, id := filepath.Split(r.URL.Path); r.Method == http.MethodGet &&
			strings.HasSuffix(path, "/builds/") && strings.HasPrefix(id, "bld-") {
			if err := store.FinishBuild(r.Context(), id, "succeeded", "", "art_cli", ""); err != nil {
				t.Errorf("finish fixture build: %v", err)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
		}
		s.ServeHTTP(w, r)
	}))
	defer server.Close()
	t.Setenv("KRUN_ENDPOINT", server.URL)
	t.Setenv("KRUN_TOKEN", "krun_cli_admin")
	dir := t.TempDir()
	config := filepath.Join(dir, "profile.yaml")
	run := func(input string, args ...string) (string, error) {
		t.Helper()
		var out, diagnostics bytes.Buffer
		err := kruncli.Run(t.Context(), append(args, "--profile", config), strings.NewReader(input), &out, &diagnostics)
		return out.String(), err
	}
	mustRun := func(args ...string) string {
		t.Helper()
		out, err := run("", args...)
		if err != nil {
			t.Fatalf("krun %v: %v (%s)", args, err, out)
		}
		return out
	}
	var worker Resource
	if err := json.Unmarshal([]byte(mustRun("workers", "create", "--org", "cli-test", "--name", "CLI Pipeline", "--locator", "cli-pipeline")), &worker); err != nil || worker.ID == "" {
		t.Fatalf("Worker identity: %+v (%v)", worker, err)
	}
	var readWorker Resource
	if err := json.Unmarshal([]byte(mustRun("workers", "get", "--id", "cli-test/workers/cli-pipeline")), &readWorker); err != nil || readWorker.ID != worker.ID {
		t.Fatalf("get Worker by full resource path: %+v (%v)", readWorker, err)
	}
	if _, err := run("", "workers", "get", "--org", "org-b", "--id", "cli-test/workers/cli-pipeline"); err == nil {
		t.Fatal("accepted conflicting organization and resource path")
	}
	target := []string{"--org", "org-a", "--worker", worker.ID}
	preview := append(append([]string{}, target...), "--env", "cli-preview")
	call := func(scope []string, args ...string) string {
		t.Helper()
		return mustRun(append(args, scope...)...)
	}
	call(preview, "workers", "environments", "create")
	vars := filepath.Join(dir, "variables.json")
	if err := os.WriteFile(vars, []byte(`{"GREETING":"hello"}`), 0600); err != nil {
		t.Fatal(err)
	}
	out := call(preview, "workers", "environments", "update", "--vars-file", vars)
	if !strings.Contains(out, `"GREETING": "hello"`) {
		t.Fatal(out)
	}
	out, err := run("fixture-secret", append([]string{"secret", "put", "TOKEN"}, preview...)...)
	if err != nil || !strings.Contains(out, "TOKEN") || strings.Contains(out, "fixture-secret") {
		t.Fatalf("secret result: %s (%v)", out, err)
	}
	call(preview, "workers", "configurations", "create", "--name", "CLI configuration")
	bound := call(target, "workers", "repository", "set", "--provider", "github", "--repository", "team/private-worker", "--private")
	if !strings.Contains(bound, `"authorization_status": "connected"`) || strings.Contains(bound, "fixture-git-grant") {
		t.Fatal(bound)
	}

	policy := filepath.Join(dir, "build-policy.yaml")
	policyYAML := "schema_version: 1\nstatements:\n  - effect: allow\n    actions: [build.start]\n    resource:\n      kind: worker\n      organization_id: org-a\n      ids: [" + worker.ID + "]\n"
	if err := os.WriteFile(policy, []byte(policyYAML), 0600); err != nil {
		t.Fatal(err)
	}
	var key struct {
		Token string `json:"key"`
	}
	if err := json.Unmarshal([]byte(mustRun("keys", "create", "--org", "org-a", "--name", "Build trigger", "--policy", policy)), &key); err != nil || key.Token == "" {
		t.Fatalf("create CI key: %v", err)
	}
	t.Setenv("KRUN_TOKEN", key.Token)

	var build WorkerBuild
	buildArgs := []string{"build", "start", "--idempotency-key", "cli-build", "--repository-revision", "1"}
	if err := json.Unmarshal([]byte(call(preview, buildArgs...)), &build); err != nil || build.Repository == nil || build.Repository.AuthorizationUser != "admin" {
		t.Fatalf("private build: %+v (%v)", build, err)
	}
	t.Setenv("KRUN_TOKEN", "krun_cli_admin")
	if err := store.Update(t.Context(), PartitionWorkers, func(st *State) error {
		st.Workers.Artifacts["art_cli"] = domain.WorkerArtifact{ID: "art_cli", Organization: "org-a", State: "finalized"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishBuild(t.Context(), build.ID, "succeeded", "", "art_cli", ""); err != nil {
		t.Fatal(err)
	}
	call(target, "build", "wait", "--build", build.ID)
	var version WorkerVersion
	if err := json.Unmarshal([]byte(call(preview, "versions", "upload")), &version); err != nil || version.ID == "" {
		t.Fatalf("version: %+v (%v)", version, err)
	}
	var op WorkerOperation
	if err := json.Unmarshal([]byte(call(preview, "versions", "deploy", version.ID+"@100%", "--wait=false")), &op); err != nil || op.Version != version.ID || op.State != "queued" {
		t.Fatalf("deployment operation: %+v (%v)", op, err)
	}
	if err := store.Update(t.Context(), PartitionWorkers, func(st *State) error {
		op.State = "superseded"
		st.Workers.Operations[op.ID] = op
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	out, err = run("", append([]string{"deployments", "wait", "--operation", op.ID}, target...)...)
	if err == nil || !strings.Contains(out, "superseded") {
		t.Fatalf("superseded deployment result: %s (%v)", out, err)
	}
	op = WorkerOperation{}
	if err := json.Unmarshal([]byte(call(preview, "deploy", "--wait=false")), &op); err != nil || op.ID == "" || op.State != "queued" {
		t.Fatalf("repository build and deploy: %+v (%v)", op, err)
	}
}
