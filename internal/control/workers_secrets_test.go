package control

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	appconfig "github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/domain"
	workerapp "github.com/krun-dev/krun/internal/workers"
)

func testWorkerSecretsKey() string {
	return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x42}, 32))
}

func TestSameWorkerSecretVersionUsesInstantEquality(t *testing.T) {
	created := time.Date(2026, time.October, 29, 3, 4, 5, 6, time.FixedZone("test", 8*60*60))
	version := WorkerSecretVersion{ID: "secret-version", Organization: "org", Worker: "worker", Environment: "production", Name: "TOKEN", ValueDigest: "digest", KeyVersion: "v1", CreatedBy: "user", Created: created}
	other := version
	other.Created = created.UTC()
	if !sameWorkerSecretVersion(version, other) {
		t.Fatal("equal instants with different locations were treated as different metadata")
	}
	other.Name = "OTHER"
	if sameWorkerSecretVersion(version, other) {
		t.Fatal("changed secret metadata was treated as equal")
	}
}

func enableWorkerSecrets(config *appconfig.WorkersConfig) {
	config.SecretsActiveKey = "test-v1"
	config.SecretsKeys = map[string]string{"test-v1": testWorkerSecretsKey()}
}

func seedPublishedWorker(t *testing.T, store *Store) WorkerVersion {
	t.Helper()
	version := WorkerVersion{
		ID: "rel_secret_base", Organization: "org-a", Worker: "svc-a", Environment: "production",
		Artifact: "art_secret", ArchiveDigest: strings.Repeat("a", 64), ModuleDigest: strings.Repeat("b", 64),
		ConfigRevision: 1, Variables: map[string]any{"PLAIN": "visible"}, Created: now(),
		SecretVersions: map[string]string{},
	}
	err := store.Update(context.Background(), PartitionWorkers, func(st *State) error {
		st.Workers.Artifacts[version.Artifact] = WorkerArtifact{
			ID: version.Artifact, Organization: "org-a", State: "finalized",
			ArchiveDigest: version.ArchiveDigest, ModuleDigest: version.ModuleDigest, Created: now(),
		}
		st.Workers.Versions[version.ID] = version
		service := st.Workers.Workers[version.Worker]
		environment := service.Environments[version.Environment]
		environment.Version, environment.Confirmed = version.ID, version.ID
		environment.Revision = 3
		environment.ConfigRevision = 1
		environment.Variables = domain.CloneWorkerVariables(version.Variables)
		environment.SecretVersions = map[string]string{}
		service.Environments[version.Environment] = environment
		st.Workers.Workers[version.Worker] = service
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return version
}

func TestWorkerSecretEncryptionAuthenticatesScope(t *testing.T) {
	config := testWorkersConfig()
	enableWorkerSecrets(config)
	encrypted, err := workerapp.EncryptWorkerSecret(config, "org", "worker", "production", "API_TOKEN", "user", "super-secret")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encrypted.Ciphertext), "super-secret") {
		t.Fatal("plaintext present in ciphertext")
	}
	plainDigest := sha256.Sum256([]byte("super-secret"))
	if encrypted.Version.ValueDigest == hex.EncodeToString(plainDigest[:]) {
		t.Fatal("secret metadata exposes a plain SHA-256 oracle")
	}
	plain, err := workerapp.DecryptWorkerSecret(config, encrypted)
	if err != nil || plain != "super-secret" {
		t.Fatalf("round trip: %q %v", plain, err)
	}
	tampered := encrypted
	tampered.Version.Worker = "other-service"
	if _, err = workerapp.DecryptWorkerSecret(config, tampered); err == nil {
		t.Fatal("ciphertext was reusable in another service scope")
	}
}

func TestWorkerSecretKeyRingValidation(t *testing.T) {
	valid := testWorkersConfig()
	enableWorkerSecrets(valid)
	if err := valid.ValidateSecretKeys(); err != nil {
		t.Fatal("valid key ring rejected:", err)
	}
	for name, mutate := range map[string]func(*appconfig.WorkersConfig){
		"missing active version": func(config *appconfig.WorkersConfig) { config.SecretsActiveKey = "missing" },
		"short key": func(config *appconfig.WorkersConfig) {
			config.SecretsKeys["test-v1"] = base64.StdEncoding.EncodeToString([]byte("short"))
		},
		"invalid base64": func(config *appconfig.WorkersConfig) { config.SecretsKeys["test-v1"] = "not-base64" },
		"invalid version": func(config *appconfig.WorkersConfig) {
			config.SecretsActiveKey = "bad version"
			config.SecretsKeys["bad version"] = config.SecretsKeys["test-v1"]
		},
	} {
		t.Run(name, func(t *testing.T) {
			config := testWorkersConfig()
			enableWorkerSecrets(config)
			mutate(config)
			if err := config.ValidateSecretKeys(); err == nil {
				t.Fatal("invalid key ring accepted")
			}
		})
	}
}

func TestWorkerSecretPutCreatesAndDeploysImmutableVersion(t *testing.T) {
	server, store, base := workerTestServer(t)
	enableWorkerSecrets(server.Config.Workers)
	baseVersion := seedPublishedWorker(t, store)
	payload := `{"value":"secret-value-never-return","expected_revision":3,"idempotency_key":"put-api-token"}`
	response := request(server, http.MethodPut, base+"/environments/production/secrets/API_TOKEN", "krun_cli_admin", payload)
	if response.Code != http.StatusAccepted {
		t.Fatalf("put: %d %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "secret-value-never-return") {
		t.Fatal("secret value leaked in put response")
	}
	var operation WorkerOperation
	if err := json.Unmarshal(response.Body.Bytes(), &operation); err != nil {
		t.Fatal(err)
	}
	if operation.Kind != "secret" || operation.Build != "" || operation.Version == baseVersion.ID {
		t.Fatalf("operation did not create a configuration-only version: %+v", operation)
	}

	retry := request(server, http.MethodPut, base+"/environments/production/secrets/API_TOKEN", "krun_cli_admin", payload)
	if retry.Code != http.StatusOK || !strings.Contains(retry.Body.String(), operation.ID) {
		t.Fatalf("idempotent retry: %d %s", retry.Code, retry.Body.String())
	}
	list := request(server, http.MethodGet, base+"/environments/production/secrets", "krun_cli_admin", "")
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), "API_TOKEN") || strings.Contains(list.Body.String(), "secret-value-never-return") {
		t.Fatalf("secret metadata list: %d %s", list.Code, list.Body.String())
	}

	var version WorkerVersion
	if err := store.View(context.Background(), PartitionWorkers, func(st *State) error {
		service := st.Workers.Workers["svc-a"]
		environment := service.Environments["production"]
		if len(environment.SecretVersions) != 1 || len(environment.SecretNames) != 1 || environment.SecretNames[0] != "API_TOKEN" {
			t.Fatalf("environment secret metadata: %+v", environment)
		}
		version = st.Workers.Versions[operation.Version]
		if version.Artifact != baseVersion.Artifact || version.SecretDigest == "" || len(version.SecretVersions) != 1 {
			t.Fatalf("version did not reuse artifact and pin secret: %+v", version)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	versionJSON, _ := json.Marshal(version)
	if strings.Contains(string(versionJSON), version.SecretVersions["API_TOKEN"]) || strings.Contains(string(versionJSON), "secret-value-never-return") {
		t.Fatalf("secret reference or value leaked in version JSON: %s", versionJSON)
	}

	unauthorized := httptest.NewRecorder()
	unauthorizedRequest := httptest.NewRequest(http.MethodGet, "/api/internal/workers/versions/"+version.ID+"/secrets", nil)
	server.serveInternal(unauthorized, unauthorizedRequest)
	if unauthorized.Code != http.StatusForbidden {
		t.Fatalf("internal endpoint allowed anonymous request: %d", unauthorized.Code)
	}
	authorized := httptest.NewRecorder()
	authorizedRequest := httptest.NewRequest(http.MethodGet, "/api/internal/workers/versions/"+version.ID+"/secrets", nil)
	authorizedRequest.Header.Set("Authorization", "Bearer "+server.Config.Workers.DefaultRuntimePool().RuntimeToken)
	authorizedRequest.Header.Set("X-Runtime-Pool", "default")
	server.serveInternal(authorized, authorizedRequest)
	if authorized.Code != http.StatusOK || !strings.Contains(authorized.Body.String(), "secret-value-never-return") || !strings.Contains(authorized.Body.String(), version.SecretDigest) {
		t.Fatalf("runtime secret delivery: %d %s", authorized.Code, authorized.Body.String())
	}
	if authorized.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("runtime secret response is cacheable: %q", authorized.Header().Get("Cache-Control"))
	}

	// Selecting a shared validator must not grant its token read access to the
	// source pool's secrets, even if it forges the source pool header.
	validator := appconfig.WorkerRuntimePool{
		ID:             "validation",
		ValidatorToken: strings.Repeat("v", 24),
	}
	server.Config.Workers.RuntimePools = append(server.Config.Workers.RuntimePools, validator)
	server.Config.Workers.ValidatorPool = validator.ID
	for _, test := range []struct {
		pool   string
		status int
	}{
		{validator.ID, http.StatusNotFound},
		{"default", http.StatusForbidden},
	} {
		response := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/internal/workers/versions/"+version.ID+"/secrets", nil)
		req.Header.Set("Authorization", "Bearer "+validator.ValidatorToken)
		req.Header.Set("X-Runtime-Pool", test.pool)
		server.serveInternal(response, req)
		if response.Code != test.status || strings.Contains(response.Body.String(), "secret-value-never-return") {
			t.Fatalf("shared validator secret access for pool %s: %d", test.pool, response.Code)
		}
	}

	// Model convergence so a delete can create the next immutable version.
	if err := store.Update(context.Background(), PartitionWorkers, func(st *State) error {
		service := st.Workers.Workers["svc-a"]
		environment := service.Environments["production"]
		environment.Version, environment.Confirmed = version.ID, version.ID
		environment.Revision = 4
		service.Environments["production"] = environment
		st.Workers.Workers["svc-a"] = service
		completed := st.Workers.Operations[operation.ID]
		completed.State = "available"
		st.Workers.Operations[operation.ID] = completed
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	deleted := request(server, http.MethodDelete, base+"/environments/production/secrets/API_TOKEN", "krun_cli_admin", `{"expected_revision":4,"idempotency_key":"delete-api-token"}`)
	if deleted.Code != http.StatusAccepted {
		t.Fatalf("delete: %d %s", deleted.Code, deleted.Body.String())
	}
	var deleteOperation WorkerOperation
	_ = json.Unmarshal(deleted.Body.Bytes(), &deleteOperation)
	if err := store.View(context.Background(), PartitionWorkers, func(st *State) error {
		service := st.Workers.Workers["svc-a"]
		if len(service.Environments["production"].SecretVersions) != 0 {
			t.Fatal("deleted secret remained in desired environment")
		}
		if next := st.Workers.Versions[deleteOperation.Version]; next.SecretDigest != "" || len(next.SecretVersions) != 0 || next.Artifact != baseVersion.Artifact {
			t.Fatalf("delete version: %+v", next)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestWorkerSecretAllowsDraftAndRejectsBindingCollision(t *testing.T) {
	server, store, base := workerTestServer(t)
	enableWorkerSecrets(server.Config.Workers)
	if response := request(server, http.MethodPut, base+"/environments/fix-demo/secrets/API_TOKEN", "krun_cli_admin", `{"value":"x","expected_revision":0,"idempotency_key":"missing-version"}`); response.Code != http.StatusCreated {
		t.Fatalf("unpublished environment rejected a secret: %d %s", response.Code, response.Body.String())
	}
	seedPublishedWorker(t, store)
	if response := request(server, http.MethodPut, base+"/environments/production/secrets/PLAIN", "krun_cli_admin", `{"value":"x","expected_revision":3,"idempotency_key":"collision"}`); response.Code != http.StatusConflict {
		t.Fatalf("plain variable collision accepted: %d %s", response.Code, response.Body.String())
	}
}
