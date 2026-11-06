package control

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/krun-dev/krun/internal/domain"
)

func configurationRequest(t *testing.T, server *Server, method, path, payload string, status int, out any) {
	t.Helper()
	response := request(server, method, path, "krun_cli_admin", payload)
	if response.Code != status {
		t.Fatalf("%s %s: %d %s", method, path, response.Code, response.Body.String())
	}
	if out != nil {
		if err := json.Unmarshal(response.Body.Bytes(), out); err != nil {
			t.Fatal(err)
		}
	}
}

func TestConfigurationVersionsSaveCopyAndPublishWithCloudflareAPI(t *testing.T) {
	server, store, cfBase := cfTestServer(t)
	original := seedPublishedWorker(t, store)
	enableWorkerSecrets(server.Config.Workers)
	base := "/api/v1/orgs/org-a/workers/svc-a"
	path := base + "/environments/production/configurations"
	payload := `{"name":"XXX 测试","message":"测试支付重试","expected_configuration_revision":1,"variables":{"PLAIN":"candidate","JSON":{"count":2}},"secrets":{"API_TOKEN":"configuration-only-fixture"},"idempotency_key":"save-config"}`
	var configuration domain.WorkerConfiguration
	configurationRequest(t, server, "POST", path, payload, http.StatusCreated, &configuration)
	var retry domain.WorkerConfiguration
	configurationRequest(t, server, "POST", path, payload, http.StatusOK, &retry)
	if retry.ID != configuration.ID || len(configuration.SecretNames) != 1 {
		t.Fatal("configuration retry or secret names lost")
	}
	response := request(server, "GET", path, "krun_cli_admin", "")
	if response.Code != 200 || strings.Contains(response.Body.String(), "configuration-only-fixture") || strings.Contains(response.Body.String(), "SecretVersions") {
		t.Fatalf("configuration metadata exposed secrets: %s", response.Body.String())
	}
	if err := store.View(context.Background(), PartitionWorkers, func(st *State) error {
		env := st.Workers.Workers["svc-a"].Environments["production"]
		if env.Version != original.ID || env.Confirmed != original.ID || env.Variables["PLAIN"] != "visible" || len(env.SecretVersions) != 0 || len(st.Workers.Operations) != 0 || len(st.Workers.Versions) != 1 {
			t.Fatal("saving a configuration changed a draft or live deployment")
		}
		if st.Workers.Configurations[configuration.ID].SecretVersions["API_TOKEN"] == "" {
			t.Fatal("secret reference was lost across state cloning")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var copied domain.WorkerConfiguration
	configurationRequest(t, server, "POST", path, `{"name":"复制测试","source_configuration_id":"`+configuration.ID+`","expected_configuration_revision":1,"variables":{"PLAIN":"copy"},"idempotency_key":"copy-config"}`, 201, &copied)
	if copied.Variables["PLAIN"] != "copy" || len(copied.SecretNames) != 1 || copied.ID == configuration.ID {
		t.Fatal("copy is not independent or dropped frozen secrets")
	}
	publishPath := path + "/" + configuration.ID + "/deployments"
	publish := `{"code_version_id":"` + original.ID + `","expected_revision":3,"expected_configuration_revision":1,"idempotency_key":"publish-config"}`
	var operation WorkerOperation
	configurationRequest(t, server, "POST", publishPath, publish, 202, &operation)
	var repeated WorkerOperation
	configurationRequest(t, server, "POST", publishPath, publish, 200, &repeated)
	if repeated.ID != operation.ID {
		t.Fatal("configuration publication was not idempotent")
	}
	var version WorkerVersion
	configurationRequest(t, server, "GET", base+"/versions/"+operation.Version, "", 200, &version)
	if version.Artifact != original.Artifact || version.Configuration != configuration.ID || version.Tag != "XXX 测试" || version.Variables["PLAIN"] != "candidate" {
		t.Fatal("configuration publication changed code or lost its snapshot")
	}
	if err := store.View(context.Background(), PartitionWorkers, func(st *State) error {
		if st.Workers.Workers["svc-a"].Environments["production"].Confirmed != original.ID {
			t.Fatal("queued publication was reported as confirmed")
		}
		if st.Workers.Configurations[copied.ID].Variables["PLAIN"] != "copy" {
			t.Fatal("publication overwrote another configuration")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	response = request(server, "GET", cfBase+"/versions/"+version.ID, "krun_cli_admin", "")
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"workers/tag":"XXX 测试"`) || !strings.Contains(response.Body.String(), `"workers/message":"测试支付重试"`) || !strings.Contains(response.Body.String(), `"name":"PLAIN"`) || !strings.Contains(response.Body.String(), `"type":"secret_text"`) || strings.Contains(response.Body.String(), "configuration-only-fixture") || strings.Contains(response.Body.String(), "configuration_id") {
		t.Fatalf("Cloudflare version contract changed: %d %s", response.Code, response.Body.String())
	}
	// The existing Cloudflare Previews Base contract remains independent of
	// the configuration library and production drafts.
	previewBase := cfPrefix + "/accounts/org-a/workers/workers/fixture"
	configurationRequest(t, server, "PATCH", previewBase, `{"previews_base_config":{"env":{"LABEL":{"type":"plain_text","text":"cf-preview-default"}}}}`, 200, nil)
	configurationRequest(t, server, "POST", previewBase+"/previews", `{"name":"cf-preview"}`, 200, nil)
	var snapshot domain.WorkerConfiguration
	configurationRequest(t, server, "POST", base+"/environments/cf-preview/configurations", `{"name":"CF 默认配置快照","expected_configuration_revision":1,"idempotency_key":"cf-snapshot"}`, 201, &snapshot)
	if snapshot.Variables["LABEL"] != "cf-preview-default" {
		t.Fatal("Cloudflare writes cannot be read by native configuration APIs")
	}
}

func TestConfigurationVersionsRejectStaleCrossEnvironmentAndUnauthorizedRequests(t *testing.T) {
	server, store, base := workerTestServer(t)
	original := seedPublishedWorker(t, store)
	path := base + "/environments/production/configurations"
	var c domain.WorkerConfiguration
	configurationRequest(t, server, "POST", path, `{"name":"baseline","expected_configuration_revision":1,"idempotency_key":"valid"}`, 201, &c)
	for _, tc := range []struct {
		method, path, token, payload string
		status                       int
	}{
		{"POST", path, "krun_cli_viewer", `{"name":"denied","idempotency_key":"denied"}`, 403},
		{"POST", path, "krun_cli_admin", `{"name":"stale","expected_configuration_revision":0,"idempotency_key":"stale"}`, 409},
		{"POST", path, "krun_cli_admin", `{"name":"   ","idempotency_key":"empty"}`, 422},
		{"POST", base + "/environments/fix-demo/configurations", "krun_cli_admin", `{"name":"other-env","source_configuration_id":"` + c.ID + `","expected_configuration_revision":1,"idempotency_key":"other"}`, 404},
		{"POST", path + "/" + c.ID + "/deployments", "krun_cli_admin", `{"code_version_id":"` + original.ID + `","expected_revision":2,"expected_configuration_revision":1,"idempotency_key":"stale-code"}`, 409},
		{"POST", path + "/" + c.ID + "/deployments", "krun_cli_admin", `{"code_version_id":"` + original.ID + `","expected_revision":3,"expected_configuration_revision":0,"idempotency_key":"stale-draft"}`, 409},
		{"POST", path + "/" + c.ID + "/deployments", "krun_cli_viewer", `{"idempotency_key":"denied-publish"}`, 403},
		{"GET", base + "/environments/missing/configurations", "krun_cli_admin", "", 404},
	} {
		response := request(server, tc.method, tc.path, tc.token, tc.payload)
		if response.Code != tc.status {
			t.Fatalf("%s %s: %d %s", tc.method, tc.path, response.Code, response.Body.String())
		}
	}
	if err := store.View(context.Background(), PartitionWorkers, func(st *State) error {
		if len(st.Workers.Configurations) != 1 || len(st.Workers.Operations) != 0 || len(st.Workers.Versions) != 1 {
			t.Fatal("rejected input persisted partial state")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestConfigurationVersionCanBeSavedBeforeCodeAndRemovedWithPreview(t *testing.T) {
	server, _, base := workerTestServer(t)
	path := base + "/environments/fix-demo/configurations"
	var c domain.WorkerConfiguration
	configurationRequest(t, server, "POST", path, `{"name":"首次部署配置","expected_configuration_revision":1,"variables":{"TEXT":"before-build"},"idempotency_key":"before-code"}`, 201, &c)
	configurationRequest(t, server, "DELETE", base+"/environments/fix-demo", "", 204, nil)
	configurationRequest(t, server, "POST", base+"/environments", `{"name":"fix-demo"}`, 201, nil)
	var result struct {
		Items []domain.WorkerConfiguration `json:"items"`
	}
	configurationRequest(t, server, "GET", path, "", 200, &result)
	if len(result.Items) != 0 {
		t.Fatal("recreated preview inherited deleted configuration history")
	}
}

func TestConfigurationRestoreReusesLatestCodeAndCodeOnlyPublicationsKeepIdentity(t *testing.T) {
	server, store, base := workerTestServer(t)
	original := seedPublishedWorker(t, store)
	path := base + "/environments/production/configurations"
	var c domain.WorkerConfiguration
	configurationRequest(t, server, "POST", path, `{"name":"历史基线","expected_configuration_revision":1,"idempotency_key":"save-baseline"}`, 201, &c)
	var newer WorkerVersion
	if err := store.Update(context.Background(), PartitionWorkers, func(st *State) error {
		artifact := st.Workers.Artifacts[original.Artifact]
		artifact.ID = "latest-code-artifact"
		st.Workers.Artifacts[artifact.ID] = artifact
		newer = original
		newer.ID, newer.Artifact = "latest-code-version", artifact.ID
		newer.Configuration = c.ID
		st.Workers.Versions[newer.ID] = newer
		worker := st.Workers.Workers["svc-a"]
		env := worker.Environments["production"]
		env.Version, env.Confirmed = newer.ID, newer.ID
		env.Revision++
		worker.Environments[env.Name] = env
		st.Workers.Workers["svc-a"] = worker
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var codeOnly WorkerVersion
	configurationRequest(t, server, "POST", base+"/environments/production/versions", `{"artifact_id":"`+newer.Artifact+`","idempotency_key":"code-only"}`, 201, &codeOnly)
	if codeOnly.Configuration != c.ID {
		t.Fatal("code-only publication lost the configuration identity")
	}
	var op WorkerOperation
	configurationRequest(t, server, "POST", path+"/"+c.ID+"/deployments", `{"code_version_id":"`+newer.ID+`","expected_revision":4,"expected_configuration_revision":1,"idempotency_key":"restore-configuration"}`, 202, &op)
	var restored WorkerVersion
	configurationRequest(t, server, "GET", base+"/versions/"+op.Version, "", 200, &restored)
	if restored.Artifact != newer.Artifact || restored.Artifact == original.Artifact || restored.Configuration != c.ID {
		t.Fatal("restoring configuration also rolled back code")
	}
	// An ordinary binding change must not incorrectly label the next version
	// as the previously selected saved configuration.
	configurationRequest(t, server, "PATCH", base+"/environments/production", `{"variables":{"PLAIN":"API change"},"expected_revision":2}`, 409, nil)
	if err := store.Update(context.Background(), PartitionWorkers, func(st *State) error {
		operation := st.Workers.Operations[op.ID]
		operation.State = "failed"
		st.Workers.Operations[op.ID] = operation
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	configurationRequest(t, server, "PATCH", base+"/environments/production", `{"variables":{"PLAIN":"API change"},"expected_revision":2}`, 200, nil)
	var changed WorkerVersion
	configurationRequest(t, server, "POST", base+"/environments/production/versions", `{"artifact_id":"`+newer.Artifact+`","idempotency_key":"changed-config"}`, 201, &changed)
	if changed.Configuration != "" {
		t.Fatal("external binding change retained a stale configuration identity")
	}
}
