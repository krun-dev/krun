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
)

func testWorkerSecretsKey() string {
	return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x42}, 32))
}

func enableWorkerSecrets(config *WorkersConfig) {
	config.SecretsActiveKey = "test-v1"
	config.SecretsKeys = map[string]string{"test-v1": testWorkerSecretsKey()}
}

func seedPublishedWorker(t *testing.T, store *Store) WorkerRelease {
	t.Helper()
	release := WorkerRelease{
		ID: "rel_secret_base", Organization: "org-a", Service: "svc-a", Environment: "production",
		Artifact: "art_secret", ArchiveDigest: strings.Repeat("a", 64), ModuleDigest: strings.Repeat("b", 64),
		ConfigRevision: 1, Variables: map[string]string{"PLAIN": "visible"}, Pool: "default", Created: now(),
		SecretVersions: map[string]string{},
	}
	err := store.Update(context.Background(), PartitionWorkers, func(st *State) error {
		st.Workers.Artifacts[release.Artifact] = WorkerArtifact{
			ID: release.Artifact, Organization: "org-a", State: "finalized",
			ArchiveDigest: release.ArchiveDigest, ModuleDigest: release.ModuleDigest, Created: now(),
		}
		st.Workers.Releases[release.ID] = release
		service := st.Workers.Services[release.Service]
		environment := service.Environments[release.Environment]
		environment.Release, environment.Confirmed = release.ID, release.ID
		environment.Revision = 3
		environment.ConfigRevision = 1
		environment.Variables = cloneStringsMap(release.Variables)
		environment.SecretVersions = map[string]string{}
		service.Environments[release.Environment] = environment
		st.Workers.Services[release.Service] = service
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return release
}

func TestWorkerSecretEncryptionAuthenticatesScope(t *testing.T) {
	config := testWorkersConfig()
	enableWorkerSecrets(config)
	encrypted, err := encryptWorkerSecret(config, "org", "service", "production", "API_TOKEN", "user", "super-secret")
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
	plain, err := decryptWorkerSecret(config, encrypted)
	if err != nil || plain != "super-secret" {
		t.Fatalf("round trip: %q %v", plain, err)
	}
	tampered := encrypted
	tampered.Version.Service = "other-service"
	if _, err = decryptWorkerSecret(config, tampered); err == nil {
		t.Fatal("ciphertext was reusable in another service scope")
	}
}

func TestWorkerSecretKeyRingValidation(t *testing.T) {
	valid := testWorkersConfig()
	enableWorkerSecrets(valid)
	if err := valid.validateSecretKeys(); err != nil {
		t.Fatal("valid key ring rejected:", err)
	}
	for name, mutate := range map[string]func(*WorkersConfig){
		"missing active version": func(config *WorkersConfig) { config.SecretsActiveKey = "missing" },
		"short key": func(config *WorkersConfig) {
			config.SecretsKeys["test-v1"] = base64.StdEncoding.EncodeToString([]byte("short"))
		},
		"invalid base64": func(config *WorkersConfig) { config.SecretsKeys["test-v1"] = "not-base64" },
		"invalid version": func(config *WorkersConfig) {
			config.SecretsActiveKey = "bad version"
			config.SecretsKeys["bad version"] = config.SecretsKeys["test-v1"]
		},
	} {
		t.Run(name, func(t *testing.T) {
			config := testWorkersConfig()
			enableWorkerSecrets(config)
			mutate(config)
			if err := config.validateSecretKeys(); err == nil {
				t.Fatal("invalid key ring accepted")
			}
		})
	}
}

func TestWorkerSecretPutCreatesAndDeploysImmutableRelease(t *testing.T) {
	server, store, base := workerTestServer(t)
	enableWorkerSecrets(server.Config.Workers)
	baseRelease := seedPublishedWorker(t, store)
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
	if operation.Kind != "secret" || operation.Build != "" || operation.Release == baseRelease.ID {
		t.Fatalf("operation did not create a configuration-only release: %+v", operation)
	}

	retry := request(server, http.MethodPut, base+"/environments/production/secrets/API_TOKEN", "krun_cli_admin", payload)
	if retry.Code != http.StatusOK || !strings.Contains(retry.Body.String(), operation.ID) {
		t.Fatalf("idempotent retry: %d %s", retry.Code, retry.Body.String())
	}
	list := request(server, http.MethodGet, base+"/environments/production/secrets", "krun_cli_admin", "")
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), "API_TOKEN") || strings.Contains(list.Body.String(), "secret-value-never-return") {
		t.Fatalf("secret metadata list: %d %s", list.Code, list.Body.String())
	}

	var release WorkerRelease
	if err := store.View(context.Background(), PartitionWorkers, func(st *State) error {
		service := st.Workers.Services["svc-a"]
		environment := service.Environments["production"]
		if len(environment.SecretVersions) != 1 || len(environment.SecretNames) != 1 || environment.SecretNames[0] != "API_TOKEN" {
			t.Fatalf("environment secret metadata: %+v", environment)
		}
		release = st.Workers.Releases[operation.Release]
		if release.Artifact != baseRelease.Artifact || release.SecretDigest == "" || len(release.SecretVersions) != 1 {
			t.Fatalf("release did not reuse artifact and pin secret: %+v", release)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	releaseJSON, _ := json.Marshal(release)
	if strings.Contains(string(releaseJSON), "secv_") || strings.Contains(string(releaseJSON), "secret-value-never-return") {
		t.Fatalf("secret reference or value leaked in release JSON: %s", releaseJSON)
	}

	unauthorized := httptest.NewRecorder()
	unauthorizedRequest := httptest.NewRequest(http.MethodGet, "/api/internal/workers/releases/"+release.ID+"/secrets", nil)
	server.serveInternal(unauthorized, unauthorizedRequest)
	if unauthorized.Code != http.StatusForbidden {
		t.Fatalf("internal endpoint allowed anonymous request: %d", unauthorized.Code)
	}
	authorized := httptest.NewRecorder()
	authorizedRequest := httptest.NewRequest(http.MethodGet, "/api/internal/workers/releases/"+release.ID+"/secrets", nil)
	authorizedRequest.Header.Set("Authorization", "Bearer "+server.Config.Workers.RuntimeToken)
	authorizedRequest.Header.Set("X-Runtime-Pool", "default")
	server.serveInternal(authorized, authorizedRequest)
	if authorized.Code != http.StatusOK || !strings.Contains(authorized.Body.String(), "secret-value-never-return") || !strings.Contains(authorized.Body.String(), release.SecretDigest) {
		t.Fatalf("runtime secret delivery: %d %s", authorized.Code, authorized.Body.String())
	}
	if authorized.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("runtime secret response is cacheable: %q", authorized.Header().Get("Cache-Control"))
	}

	// Model convergence so a delete can create the next immutable version.
	if err := store.Update(context.Background(), PartitionWorkers, func(st *State) error {
		service := st.Workers.Services["svc-a"]
		environment := service.Environments["production"]
		environment.Release, environment.Confirmed = release.ID, release.ID
		environment.Revision = 4
		service.Environments["production"] = environment
		st.Workers.Services["svc-a"] = service
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
		service := st.Workers.Services["svc-a"]
		if len(service.Environments["production"].SecretVersions) != 0 {
			t.Fatal("deleted secret remained in desired environment")
		}
		if next := st.Workers.Releases[deleteOperation.Release]; next.SecretDigest != "" || len(next.SecretVersions) != 0 || next.Artifact != baseRelease.Artifact {
			t.Fatalf("delete release: %+v", next)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestWorkerSecretRejectsBindingCollisionAndMissingRelease(t *testing.T) {
	server, store, base := workerTestServer(t)
	enableWorkerSecrets(server.Config.Workers)
	if response := request(server, http.MethodPut, base+"/environments/fix-demo/secrets/API_TOKEN", "krun_cli_admin", `{"value":"x","expected_revision":0,"idempotency_key":"missing-release"}`); response.Code != http.StatusConflict {
		t.Fatalf("unpublished environment accepted a secret: %d %s", response.Code, response.Body.String())
	}
	seedPublishedWorker(t, store)
	if response := request(server, http.MethodPut, base+"/environments/production/secrets/PLAIN", "krun_cli_admin", `{"value":"x","expected_revision":3,"idempotency_key":"collision"}`); response.Code != http.StatusConflict {
		t.Fatalf("plain variable collision accepted: %d %s", response.Code, response.Body.String())
	}
}
