package control

import (
	"context"
	"encoding/json"
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
	appconfig "github.com/krun-dev/krun/internal/config"
)

func TestHyperdriveControlAndRuntimeBindings(t *testing.T) {
	s, store, workerPath := cfTestServer(t)
	enableWorkerSecrets(s.Config.Workers)
	s.Config.Workers.Hyperdrive = &appconfig.WorkerHyperdriveConfig{Address: "proxy.example.test:6432"}
	pool := &s.Config.Workers.RuntimePools[0]
	pool.PublicEgress = "direct"
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

	input, err := cfcompat.BindingInput(cfcompat.UploadMetadata{Bindings: []cfcompat.Binding{{Type: "hyperdrive", Name: "DB", ID: id}}}, false)
	if err != nil || input.HyperdriveBindings == nil || (*input.HyperdriveBindings)["DB"] != id {
		t.Fatalf("Wrangler binding metadata was not parsed: %v", err)
	}
	version := WorkerVersion{
		ID: "rel-hyperdrive", Organization: "org-a", Worker: "svc-a", Environment: "production",
		Artifact: "art-hyperdrive", ArchiveDigest: "archive", ModuleDigest: "module",
		Created: now(), HyperdriveBindings: map[string]string{"DB": id},
	}
	if err := store.Update(ctx, PartitionWorkers, func(st *State) error {
		worker := st.Workers.Workers[version.Worker]
		env := worker.Environments[version.Environment]
		env.Version = version.ID
		env.HyperdriveBindings = version.HyperdriveBindings
		worker.Environments[version.Environment] = env
		st.Workers.Workers[worker.Worker] = worker
		st.Workers.Artifacts[version.Artifact] = WorkerArtifact{
			ID: version.Artifact, Organization: version.Organization, State: "finalized",
			ArchiveDigest: version.ArchiveDigest, ModuleDigest: version.ModuleDigest, Created: now(),
		}
		st.Workers.Versions[version.ID] = version
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	read := request(s, http.MethodGet, workerPath+"/versions/"+version.ID, "krun_cli_admin", "")
	if read.Code != 200 || !strings.Contains(read.Body.String(), id) {
		t.Fatalf("Wrangler Version binding was lost: %d %s", read.Code, read.Body.String())
	}

	app := s.workerApplication(nil)
	dsns, err := app.HyperdriveConnectionStrings(version)
	if err != nil || strings.Contains(dsns["DB"], "fixture-origin-password") {
		t.Fatalf("invalid runtime connection string: %v", err)
	}
	dsn, err := url.Parse(dsns["DB"])
	if err != nil || dsn.Host != "proxy.example.test:6432" {
		t.Fatal("runtime connection string does not target the proxy")
	}
	token, _ := dsn.User.Password()
	target, password, public, _, err := app.ResolveHyperdriveSession(ctx, dsn.User.Username(), token)
	if err != nil || target.ID != id || password != "fixture-origin-password" || !public {
		t.Fatalf("could not authorize the persisted Version binding: %v", err)
	}

	updated := request(s, http.MethodPatch, configs+"/"+id, "krun_cli_admin", `{"origin":{"password":"fixture-new-password"}}`)
	if updated.Code != 200 || strings.Contains(updated.Body.String(), "fixture-new-password") {
		t.Fatalf("rotate origin credential: %d %s", updated.Code, updated.Body.String())
	}
	_, password, _, _, err = app.ResolveHyperdriveSession(ctx, dsn.User.Username(), token)
	if err != nil || password != "fixture-new-password" {
		t.Fatalf("new sessions must use the rotated credential: %v", err)
	}

	pool.PublicEgress = "deny"
	pool.PrivateEgress = "deny"
	if _, _, _, _, err := app.ResolveHyperdriveSession(ctx, dsn.User.Username(), token); err == nil {
		t.Fatal("closing both pool egress modes should deny the active binding")
	}
	pool.PublicEgress = "direct"
	deleted := request(s, http.MethodDelete, configs+"/"+id, "krun_cli_admin", "")
	if deleted.Code != 200 {
		t.Fatalf("revoke configuration: %d %s", deleted.Code, deleted.Body.String())
	}
	if _, _, _, _, err := app.ResolveHyperdriveSession(ctx, dsn.User.Username(), token); err == nil {
		t.Fatal("deleted configuration still authorizes connections")
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
	s.Config.Workers.Hyperdrive = &appconfig.WorkerHyperdriveConfig{Address: "proxy.example.test:6432"}
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
