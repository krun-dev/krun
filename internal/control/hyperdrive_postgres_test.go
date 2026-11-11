package control

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/krun-dev/krun/internal/adapters/cfcompat"
)

func TestHyperdriveControlAndRuntimeBindings(t *testing.T) {
	s, store, workerPath := cfTestServer(t)
	enableWorkerSecrets(s.Config.Workers)
	otherPool := s.Config.Workers.RuntimePools[0]
	otherPool.ID = "other-pool"
	otherPool.RuntimeToken = "fixture-other-runtime-credential"
	otherPool.PrivateEgress = "direct"
	s.Config.Workers.RuntimePools = append(s.Config.Workers.RuntimePools, otherPool)
	pool := &s.Config.Workers.RuntimePools[0]
	pool.PrivateEgress = "direct"
	ctx := t.Context()
	configs := cfPrefix + "/accounts/org-a/hyperdrive/configs"
	response := request(s, http.MethodPost, configs, "krun_cli_admin", `{
		"name":"orders",
		"origin":{"scheme":"postgresql","host":"orders.example.test","port":5432,"database":"orders","user":"app","password":"fixture-origin-password"}
	}`)
	if response.Code != 200 || strings.Contains(response.Body.String(), "fixture-origin-password") {
		t.Fatalf("create Hyperdrive: %d %s", response.Code, response.Body.String())
	}
	var created struct {
		Result struct{ ID string }
	}
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil || len(created.Result.ID) != 32 {
		t.Fatalf("invalid Hyperdrive ID: %v", err)
	}
	id := created.Result.ID
	list := request(s, http.MethodGet, "/api/v1/orgs/org-a/hyperdrive", "krun_cli_admin", "")
	if list.Code != 200 || !strings.Contains(list.Body.String(), id) || strings.Contains(list.Body.String(), "fixture-origin-password") {
		t.Fatalf("console list: %d %s", list.Code, list.Body.String())
	}
	if _, err := store.GetHyperdrive(ctx, "org-b", id); err == nil {
		t.Fatal("a different organization can read the configuration")
	}

	native := "/api/v1/orgs/org-a/workers/svc-a"
	environments := request(s, http.MethodGet, native+"/environments", "krun_cli_admin", "")
	var environmentList struct {
		Items []struct {
			Name                  string `json:"name"`
			ConfigurationRevision int    `json:"configuration_revision"`
		} `json:"items"`
	}
	if err := json.Unmarshal(environments.Body.Bytes(), &environmentList); err != nil {
		t.Fatal(err)
	}
	var configurationRevision int
	for _, environment := range environmentList.Items {
		if environment.Name == "production" {
			configurationRevision = environment.ConfigurationRevision
		}
	}
	configuration := request(s, http.MethodPost, native+"/environments/production/configurations", "krun_cli_admin", fmt.Sprintf(`{
		"name":"hyperdrive-config",
		"variables":{},
		"hyperdrive_bindings":{"DB":%q},
		"expected_configuration_revision":%d,
		"idempotency_key":"hyperdrive-config-%d"
	}`, id, configurationRevision, time.Now().UnixNano()))
	if configuration.Code != http.StatusCreated || !strings.Contains(configuration.Body.String(), id) {
		t.Fatalf("configuration with Hyperdrive binding: %d %s", configuration.Code, configuration.Body.String())
	}
	preview := request(s, http.MethodPatch, native+"/previews-base", "krun_cli_admin", fmt.Sprintf(`{
		"hyperdrive_bindings":{"DB":%q},
		"expected_revision":0
	}`, id))
	if preview.Code != http.StatusOK || !strings.Contains(preview.Body.String(), id) {
		t.Fatalf("Previews Base with Hyperdrive binding: %d %s", preview.Code, preview.Body.String())
	}

	input, err := cfcompat.BindingInput(cfcompat.UploadMetadata{Bindings: []cfcompat.Binding{{Type: "hyperdrive", Name: "DB", ID: id}}}, false)
	if err != nil || input.HyperdriveBindings == nil || (*input.HyperdriveBindings)["DB"] != id {
		t.Fatalf("Wrangler binding metadata was not parsed: %v", err)
	}
	version := WorkerVersion{
		ID: "rel-hyperdrive", Organization: "org-a", Worker: "svc-a", Environment: "production",
		Artifact: "art-hyperdrive", ArchiveDigest: "archive", ModuleDigest: "module",
		Created: now(), HyperdriveBindings: map[string]string{"DB": id, "DB_ALIAS": id},
	}
	unbound := version
	unbound.ID = "rel-without-hyperdrive"
	unbound.HyperdriveBindings = map[string]string{}
	if err := store.Update(ctx, PartitionWorkers, func(st *State) error {
		worker := st.Workers.Workers[version.Worker]
		env := worker.Environments[version.Environment]
		env.Enabled = true
		env.Version = version.ID
		env.HyperdriveBindings = version.HyperdriveBindings
		worker.Environments[version.Environment] = env
		st.Workers.Workers[worker.Worker] = worker
		st.Workers.Artifacts[version.Artifact] = WorkerArtifact{
			ID: version.Artifact, Organization: version.Organization, State: "finalized",
			ArchiveDigest: version.ArchiveDigest, ModuleDigest: version.ModuleDigest, Created: now(),
		}
		st.Workers.Versions[version.ID] = version
		st.Workers.Versions[unbound.ID] = unbound
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	read := request(s, http.MethodGet, workerPath+"/versions/"+version.ID, "krun_cli_admin", "")
	if read.Code != 200 || !strings.Contains(read.Body.String(), id) {
		t.Fatalf("Wrangler Version binding was lost: %d %s", read.Code, read.Body.String())
	}

	path := "/api/internal/workers/versions/" + version.ID + "/hyperdrive-direct"
	call := func(path, token, poolID string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Runtime-Pool", poolID)
		out := httptest.NewRecorder()
		s.serveInternal(out, req)
		return out
	}
	for _, test := range []struct {
		path, token, poolID string
		status              int
	}{
		{path, pool.ValidatorToken, pool.ID, http.StatusForbidden},
		{path, pool.RuntimeToken, otherPool.ID, http.StatusForbidden},
		{path, otherPool.RuntimeToken, otherPool.ID, http.StatusNotFound},
		{strings.TrimSuffix(path, "-direct"), pool.RuntimeToken, pool.ID, http.StatusNotFound},
		{strings.Replace(path, version.ID, "missing-version", 1), pool.RuntimeToken, pool.ID, http.StatusNotFound},
	} {
		if out := call(test.path, test.token, test.poolID); out.Code != test.status {
			t.Fatalf("credential authorization: status %d, want %d", out.Code, test.status)
		}
	}

	checkDSN := func(wantPassword string) {
		t.Helper()
		out := call(path, pool.RuntimeToken, pool.ID)
		if out.Code != http.StatusOK || out.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("expected an uncached credential response, status %d", out.Code)
		}
		var dsns map[string]string
		if err := json.Unmarshal(out.Body.Bytes(), &dsns); err != nil {
			t.Fatal("invalid credential response")
		}
		if len(dsns) != 2 || dsns["DB_ALIAS"] != dsns["DB"] {
			t.Fatal("credential response must preserve both binding names")
		}
		dsn, err := url.Parse(dsns["DB"])
		if err != nil || dsn.User == nil {
			t.Fatal("invalid PostgreSQL connection string")
		}
		password, _ := dsn.User.Password()
		if dsn.Scheme != "postgresql" || dsn.Host != "orders.example.test:5432" ||
			dsn.Path != "/orders" || dsn.User.Username() != "app" || password != wantPassword ||
			dsn.Query().Get("sslmode") != "verify-full" {
			t.Fatal("connection string must target the source database with verified TLS")
		}
	}
	checkDSN("fixture-origin-password")

	// Cold Hyperdrive resolution reads only the version and database bindings.
	// Make the unrelated R2 table unavailable in this test's isolated schema.
	db := postgresTestPool(store)
	execSQL := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	execSQL("ALTER TABLE krun_worker_version_r2 RENAME TO unavailable_worker_version_r2")
	checkDSN("fixture-origin-password")
	execSQL("ALTER TABLE unavailable_worker_version_r2 RENAME TO krun_worker_version_r2")

	unboundPath := strings.Replace(path, version.ID, unbound.ID, 1)
	if out := call(unboundPath, pool.RuntimeToken, pool.ID); out.Code != http.StatusOK || strings.TrimSpace(out.Body.String()) != "{}" {
		t.Fatalf("an unbound version should return an empty object, status %d", out.Code)
	}
	execSQL("UPDATE krun_worker_artifacts SET state='pending' WHERE artifact_id=$1", version.Artifact)
	if out := call(path, pool.RuntimeToken, pool.ID); out.Code != http.StatusNotFound {
		t.Fatalf("an unfinalized artifact must not issue credentials, status %d", out.Code)
	}
	execSQL("UPDATE krun_worker_artifacts SET state='finalized' WHERE artifact_id=$1", version.Artifact)
	setAlias := "UPDATE krun_worker_versions SET hyperdrive_bindings=jsonb_set(hyperdrive_bindings, '{DB_ALIAS}', to_jsonb($2::text)) WHERE version_id=$1"
	execSQL(setAlias, version.ID, "missing-config")
	if out := call(path, pool.RuntimeToken, pool.ID); out.Code != http.StatusNotFound {
		t.Fatalf("a missing binding must fail the whole request, status %d", out.Code)
	}
	execSQL(setAlias, version.ID, id)

	setCurrent := "UPDATE krun_worker_environments SET version_id=$3 WHERE worker_id=$1 AND env_name=$2"
	execSQL(setCurrent, version.Worker, version.Environment, unbound.ID)
	execSQL(`INSERT INTO krun_worker_deployments
		(deployment_id, org_id, worker_id, env_name, version_id, operation_id, created_at, expires_at, committed_at)
		VALUES ('hyperdrive-deployment', $1, $2, $3, $4, 'hyperdrive-operation',
			now()-interval '2 hours', now()+interval '1 hour', now())`,
		version.Organization, version.Worker, version.Environment, version.ID)
	checkDSN("fixture-origin-password")
	execSQL("UPDATE krun_worker_deployments SET expires_at=now()-interval '1 hour' WHERE deployment_id='hyperdrive-deployment'")
	if out := call(path, pool.RuntimeToken, pool.ID); out.Code != http.StatusNotFound {
		t.Fatalf("an expired deployment must not issue credentials, status %d", out.Code)
	}
	execSQL(setCurrent, version.Worker, version.Environment, version.ID)

	updated := request(s, http.MethodPatch, configs+"/"+id, "krun_cli_admin", `{"origin":{"password":"fixture-new-password"}}`)
	if updated.Code != 200 || strings.Contains(updated.Body.String(), "fixture-new-password") {
		t.Fatalf("rotate origin credential: %d", updated.Code)
	}
	checkDSN("fixture-new-password")

	pool.PublicEgress = "deny"
	pool.PrivateEgress = "deny"
	if out := call(path, pool.RuntimeToken, pool.ID); out.Code != http.StatusForbidden {
		t.Fatalf("pool without egress received credentials, status %d", out.Code)
	}
	pool.PrivateEgress = "direct"
	deleted := request(s, http.MethodDelete, configs+"/"+id, "krun_cli_admin", "")
	if deleted.Code != 200 {
		t.Fatalf("delete configuration: %d", deleted.Code)
	}
	if out := call(path, pool.RuntimeToken, pool.ID); out.Code != http.StatusNotFound {
		t.Fatalf("deleted configuration still issued credentials, status %d", out.Code)
	}
}

func TestOriginalWranglerHyperdrive(t *testing.T) {
	wrangler, err := filepath.Abs(filepath.Join("..", "..", "workers", "builder", "node_modules", ".bin", "wrangler"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(wrangler); err != nil {
		t.Skip("install the Workers builder dependencies to test original Wrangler")
	}
	s, store, _ := cfTestServer(t)
	enableWorkerSecrets(s.Config.Workers)
	server := httptest.NewServer(s)
	defer server.Close()
	dir := t.TempDir()
	config := filepath.Join(dir, "wrangler.jsonc")
	if err := os.WriteFile(config, []byte(`{"name":"fixture","main":"index.js","compatibility_date":"2026-09-28"}`), 0600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, wrangler, append([]string{"hyperdrive"}, args...)...)
		command.Dir = dir
		command.Env = append(os.Environ(), "CI=true", "WRANGLER_SEND_METRICS=false", "CLOUDFLARE_ACCOUNT_ID=org-a",
			"CLOUDFLARE_API_BASE_URL="+server.URL+cfPrefix, "CLOUDFLARE_API_TOKEN=krun_cli_admin")
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("Wrangler %s failed: %v\n%s", args[0], err, output)
		}
		return string(output)
	}
	run("create", "wrangler-orders", "--connection-string",
		"postgresql://app:fixture-wrangler-password@orders.example.test:5432/orders", "--binding", "DB", "--update-config")
	configurations, err := store.ListHyperdrive(t.Context(), "org-a")
	if err != nil || len(configurations) != 1 {
		t.Fatalf("Wrangler did not persist the configuration: %v", err)
	}
	item := configurations[0]
	raw, err := os.ReadFile(config)
	if err != nil || !strings.Contains(string(raw), item.ID) || !strings.Contains(string(raw), "\"DB\"") ||
		strings.Contains(string(raw), "fixture-wrangler-password") {
		t.Fatalf("Wrangler did not write the binding safely: %v", err)
	}
	if !strings.Contains(run("list"), item.ID) || !strings.Contains(run("get", item.ID), "orders.example.test") {
		t.Fatal("Wrangler could not list and read the configuration")
	}
	run("update", item.ID, "--password", "fixture-wrangler-rotated")
	run("delete", item.ID)
	if _, err := store.GetHyperdrive(t.Context(), "org-a", item.ID); err == nil {
		t.Fatal("Wrangler delete did not revoke the configuration")
	}
}
