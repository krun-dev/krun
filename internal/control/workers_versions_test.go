package control

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/krun-dev/krun/internal/domain"
	workerapp "github.com/krun-dev/krun/internal/workers"
)

func TestVersionUploadFreezesConfigurationWithoutDeploying(t *testing.T) {
	server, store, base := workerTestServer(t)
	active := seedPublishedWorker(t, store)
	response := request(server, "POST", base+"/environments/production/versions", "krun_cli_admin", `{"artifact_id":"art_secret","idempotency_key":"upload-version"}`)
	if response.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", response.Code, response.Body.String())
	}
	var version WorkerVersion
	if err := json.Unmarshal(response.Body.Bytes(), &version); err != nil {
		t.Fatal(err)
	}
	if version.ID == active.ID || version.Variables["PLAIN"] != "visible" {
		t.Fatal("version snapshot not created")
	}
	if err := store.Update(context.Background(), PartitionWorkers, func(st *State) error {
		env := st.Workers.Workers["svc-a"].Environments["production"]
		if env.Version != active.ID || len(st.Workers.Operations) != 0 || len(st.Workers.Deployments) != 0 {
			t.Fatal("upload changed traffic")
		}
		env.Variables = map[string]any{"PLAIN": "changed after upload"}
		env.ConfigRevision++
		service := st.Workers.Workers["svc-a"]
		service.Environments["production"] = env
		st.Workers.Workers["svc-a"] = service
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	response = request(server, "POST", base+"/environments/production/deployments", "krun_cli_admin", `{"version_id":"`+version.ID+`","expected_revision":3,"idempotency_key":"deploy-saved"}`)
	if response.Code != http.StatusAccepted {
		t.Fatalf("deploy: %d %s", response.Code, response.Body.String())
	}
	var op WorkerOperation
	_ = json.Unmarshal(response.Body.Bytes(), &op)
	if op.Version != version.ID {
		t.Fatal("deployment rebuilt version")
	}
	if err := store.View(context.Background(), PartitionWorkers, func(st *State) error {
		if st.Workers.Versions[op.Version].Variables["PLAIN"] != "visible" {
			t.Fatal("frozen config changed")
		}
		d := st.Workers.Deployments[workerapp.DeploymentID(op.ID)]
		if d.Version != version.ID || d.Committed != nil {
			t.Fatal("candidate became routable before validation")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestBuildDeploymentFreezesJSONAndVersionMetadata(t *testing.T) {
	server, store, base := workerTestServer(t)
	seedPublishedWorker(t, store)
	if err := store.Update(context.Background(), PartitionWorkers, func(st *State) error {
		service := st.Workers.Workers["svc-a"]
		env := service.Environments["production"]
		env.VersionMetadata = "META"
		env.Variables = map[string]any{"CONFIG": map[string]any{"nullable": nil, "items": []any{true, "two"}}}
		service.Environments["production"] = env
		st.Workers.Workers["svc-a"] = service
		st.Workers.Builds["build-meta"] = WorkerBuild{ID: "build-meta", Organization: "org-a", Worker: "svc-a", Environment: "production", State: "succeeded", Artifact: "art_secret", Created: now()}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	response := request(server, "POST", base+"/environments/production/deployments", "krun_cli_admin", `{"build_id":"build-meta","expected_revision":3,"idempotency_key":"native-metadata"}`)
	if response.Code != 202 {
		t.Fatalf("native build deployment: %d %s", response.Code, response.Body.String())
	}
	var op WorkerOperation
	if err := json.Unmarshal(response.Body.Bytes(), &op); err != nil {
		t.Fatal(err)
	}
	if err := store.View(context.Background(), PartitionWorkers, func(st *State) error {
		version := st.Workers.Versions[op.Version]
		if version.VersionMetadata != "META" || version.Variables["CONFIG"].(map[string]any)["nullable"] != nil {
			t.Fatal("native deployment lost frozen bindings", version)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
func TestPreviewsBaseSecretsAreCopiedOnlyAtCreation(t *testing.T) {
	server, store, base := workerTestServer(t)
	enableWorkerSecrets(server.Config.Workers)
	for _, payload := range []string{`{"secrets":{"apiToken":"base-first"},"expected_revision":0,"idempotency_key":"base-first"}`, `{"secrets":{"apiToken":"base-second"},"expected_revision":0,"idempotency_key":"base-second"}`} {
		response := request(server, "PATCH", base+"/previews-base/secrets", "krun_cli_admin", payload)
		if response.Code != http.StatusCreated {
			t.Fatalf("base: %d %s", response.Code, response.Body.String())
		}
		name := "first"
		if strings.Contains(payload, "base-second") {
			name = "second"
		}
		response = request(server, "POST", base+"/environments", "krun_cli_admin", `{"name":"`+name+`"}`)
		if response.Code != http.StatusCreated {
			t.Fatalf("preview: %d %s", response.Code, response.Body.String())
		}
	}
	if err := store.View(context.Background(), PartitionWorkers, func(st *State) error {
		service := st.Workers.Workers["svc-a"]
		a, b := service.Environments["first"], service.Environments["second"]
		if a.SecretVersions["apiToken"] == "" || a.SecretVersions["apiToken"] == b.SecretVersions["apiToken"] {
			t.Fatal("active preview followed base changes")
		}
		if len(service.Environments["production"].SecretVersions) != 0 {
			t.Fatal("base secret leaked into production")
		}
		if len(st.Workers.Operations) != 0 {
			t.Fatal("base configuration deployed production")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
func TestPinnedDeploymentRoutesKeepTheirVersion(t *testing.T) {
	server, store, _ := workerTestServer(t)
	active := seedPublishedWorker(t, store)
	var oldHost string
	if err := store.Update(context.Background(), PartitionWorkers, func(st *State) error {
		at := time.Now().UTC()
		old := domain.WorkerDeployment{ID: "dep-first", Organization: "org-a", Worker: "svc-a", Environment: "production", Version: active.ID, Operation: "op-first", Created: at, Expires: at.Add(time.Hour), Committed: &at}
		st.Workers.Deployments[old.ID] = old
		service := st.Workers.Workers["svc-a"]
		env := service.Environments["production"]
		newVersion := active
		newVersion.ID = "new-version"
		st.Workers.Versions[newVersion.ID] = newVersion
		env.Version = newVersion.ID
		service.Environments["production"] = env
		st.Workers.Workers["svc-a"] = service
		projection := workerapp.WorkerConfigFromState(st, server.Config.Workers)
		for _, route := range projection.Routes {
			if strings.Contains(route.Host, workerapp.DeploymentEnvironment(old.ID)) {
				oldHost = route.Host
				if route.Version != active.ID {
					t.Fatal("pinned URL followed current deployment")
				}
			}
		}
		if oldHost == "" {
			t.Fatal("pinned route missing")
		}
		delete(service.Environments, "production")
		st.Workers.Workers["svc-a"] = service
		projection = workerapp.WorkerConfigFromState(st, server.Config.Workers)
		for _, route := range projection.Routes {
			if route.Host == oldHost {
				t.Fatal("deleted target still routable")
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
