package control

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/krun-dev/krun/internal/domain"
	workerapp "github.com/krun-dev/krun/internal/workers"
)

func TestVersionUploadFreezesConfigurationWithoutDeploying(t *testing.T) {
	server, store, base := workerTestServer(t)
	active := seedPublishedWorker(t, store)
	if err := store.Update(context.Background(), PartitionIAM, func(st *State) error {
		user := st.Users["admin"]
		user.Name = "Release Author"
		st.Users[user.ID] = user
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	response := request(server, "POST", base+"/environments/production/versions", "krun_cli_admin", `{"artifact_id":"art_secret","idempotency_key":"upload-version"}`)
	if response.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", response.Code, response.Body.String())
	}
	var version WorkerVersion
	if err := json.Unmarshal(response.Body.Bytes(), &version); err != nil {
		t.Fatal(err)
	}
	if version.CreatedBy != "admin" || version.CreatedByName != "Release Author" {
		t.Fatalf("missing version author: %+v", version)
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

func TestVersionUploadChecksReviewedConfigurationAndRetriesIdempotently(t *testing.T) {
	server, store, base := workerTestServer(t)
	active := seedPublishedWorker(t, store)
	path := base + "/environments/production/versions"
	staleInput := `{"artifact_id":"art_secret","expected_configuration_revision":0,"idempotency_key":"stale-review"}`
	response := request(server, "POST", path, "krun_cli_admin", staleInput)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "REVISION_CONFLICT") {
		t.Fatalf("stale review: %d %s", response.Code, response.Body.String())
	}
	if err := store.View(context.Background(), PartitionWorkers, func(st *State) error {
		if len(st.Workers.Versions) != 1 || len(st.Workers.Operations) != 0 {
			t.Fatal("stale review created a version or deployment")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	input := `{"artifact_id":"art_secret","expected_configuration_revision":1,"tag":"reviewed-release","message":"Ready for a later deployment","idempotency_key":"reviewed-version"}`
	response = request(server, "POST", path, "krun_cli_admin", input)
	if response.Code != http.StatusCreated {
		t.Fatalf("reviewed upload: %d %s", response.Code, response.Body.String())
	}
	var version WorkerVersion
	if err := json.Unmarshal(response.Body.Bytes(), &version); err != nil {
		t.Fatal(err)
	}
	if version.ConfigRevision != 1 || version.Tag != "reviewed-release" || version.Message != "Ready for a later deployment" {
		t.Fatalf("reviewed snapshot: %+v", version)
	}
	response = request(server, "GET", base+"/overview", "krun_cli_admin", "")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), version.ID) {
		t.Fatalf("saved version missing from overview: %d %s", response.Code, response.Body.String())
	}

	if err := store.Update(context.Background(), PartitionWorkers, func(st *State) error {
		worker := st.Workers.Workers[active.Worker]
		env := worker.Environments["production"]
		env.Variables = map[string]any{"PLAIN": "changed after review"}
		env.ConfigRevision++
		worker.Environments["production"] = env
		st.Workers.Workers[active.Worker] = worker
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	response = request(server, "POST", path, "krun_cli_admin", input)
	if response.Code != http.StatusOK {
		t.Fatalf("retry after configuration change: %d %s", response.Code, response.Body.String())
	}
	var retry WorkerVersion
	if err := json.Unmarshal(response.Body.Bytes(), &retry); err != nil {
		t.Fatal(err)
	}
	if retry.ID != version.ID || retry.Variables["PLAIN"] != "visible" {
		t.Fatal("retry created or changed the frozen version")
	}
	if err := store.View(context.Background(), PartitionWorkers, func(st *State) error {
		if len(st.Workers.Versions) != 2 || len(st.Workers.Operations) != 0 || st.Workers.Workers[active.Worker].Environments["production"].Version != active.ID {
			t.Fatal("version creation or retry changed live traffic")
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
		if version.CreatedBy != "admin" {
			t.Fatalf("build deployment lost author: %+v", version)
		}
		if version.VersionMetadata != "META" || version.Variables["CONFIG"].(map[string]any)["nullable"] != nil {
			t.Fatal("native deployment lost frozen bindings", version)
		}
		version.ID, version.CreatedBy, version.CreatedByName = "", "", ""
		version.Created = time.Time{}
		raw, err := json.Marshal(version)
		if err != nil {
			return err
		}
		if op.Version != "rel-"+digest(string(raw)) {
			t.Fatal("build deployment version ID no longer matches its snapshot digest")
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
		projection := workerapp.EdgeConfigFromState(st, server.Config.Workers)
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
		projection = workerapp.EdgeConfigFromState(st, server.Config.Workers)
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

func TestVersionAndDeploymentPagination(t *testing.T) {
	server, store, base := workerTestServer(t)
	active := seedPublishedWorker(t, store)
	created := time.Now().UTC()
	if err := store.Update(context.Background(), PartitionWorkers, func(st *State) error {
		for i := 0; i < 5; i++ {
			version := active
			version.ID = fmt.Sprintf("page-%02d", i)
			// Equal timestamps must not lose items at page boundaries.
			version.Created = created
			version.Tag = "Candidate"
			version.CreatedBy = "admin"
			version.CreatedByName = "Release Author"
			st.Workers.Versions[version.ID] = version
			deployment := domain.WorkerDeployment{
				ID: "dep-" + version.ID, Organization: version.Organization, Worker: version.Worker,
				Environment: version.Environment, Version: version.ID, Operation: "op-" + version.ID,
				Created: created, Expires: created.Add(time.Hour),
			}
			st.Workers.Deployments[deployment.ID] = deployment
		}
		hidden := active
		hidden.ID = "page-other-environment"
		hidden.Environment = "fix-demo"
		hidden.Tag = "Candidate"
		st.Workers.Versions[hidden.ID] = hidden
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"versions", "deployments"} {
		seen := map[string]bool{}
		after := ""
		for _, count := range []int{2, 2, 1} {
			var page struct {
				Items []struct{ ID, Environment string } `json:"items"`
				Next  string                             `json:"next_after"`
			}
			response := request(server, "GET", base+"/"+endpoint+"?environment=production&q=PAGE-&limit=2&after="+url.QueryEscape(after), "krun_cli_admin", "")
			if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil || response.Code != 200 || len(page.Items) != count {
				t.Fatalf("%s page: %d %s (%v)", endpoint, response.Code, response.Body.String(), err)
			}
			for _, item := range page.Items {
				if seen[item.ID] || item.Environment != "production" {
					t.Fatalf("duplicate or foreign item: %+v", item)
				}
				seen[item.ID] = true
			}
			after = page.Next
		}
		if after != "" || len(seen) != 5 {
			t.Fatalf("incomplete %s pages: %v, %q", endpoint, seen, after)
		}
		for _, query := range []string{"limit=0", "limit=201", "limit=bad", "after=bad"} {
			response := request(server, "GET", base+"/"+endpoint+"?"+query, "krun_cli_admin", "")
			if response.Code != 422 {
				t.Fatalf("invalid %s page: %d %s", endpoint, response.Code, response.Body.String())
			}
		}
	}
	response := request(server, "GET", base+"/versions?environment=production&q=release%20author&limit=2", "krun_cli_admin", "")
	var page struct {
		Items []WorkerVersion `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil || response.Code != 200 || len(page.Items) != 2 || page.Items[0].CreatedByName != "Release Author" {
		t.Fatalf("author search failed: %d %s", response.Code, response.Body.String())
	}
	response = request(server, "GET", base+"/versions/page-04", "krun_cli_admin", "")
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"created_by_name":"Release Author"`) {
		t.Fatalf("off-page detail missing author: %d %s", response.Code, response.Body.String())
	}
	response = request(server, "GET", "/api/v1/orgs/org-b/workers/svc-b/versions/page-04", "krun_cli_admin", "")
	if response.Code != 404 {
		t.Fatalf("cross-worker detail: %d %s", response.Code, response.Body.String())
	}
}
