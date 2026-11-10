package control

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/krun-dev/krun/internal/adapters/storagecredentials"
	appconfig "github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/domain"
	workerapp "github.com/krun-dev/krun/internal/workers"
)

func enableTestObjectStorage(t *testing.T, s *Server) {
	t.Helper()
	s.Config.Workers.RuntimePools[0].ValidatorToken = "fixture-validator-token-for-tests"
	cursorKey := sha256.Sum256([]byte("object storage cursor integration test"))
	s.Config.Workers.ObjectStorage = &appconfig.WorkerObjectStorageConfig{
		DefaultStore: "primary",
		CursorKey:    base64.RawURLEncoding.EncodeToString(cursorKey[:]),
		Stores:       []appconfig.WorkerObjectStore{{ID: "primary", Endpoint: "https://storage.example.com", Region: "us-east-1", Bucket: "worker-data", RootPrefix: "worker-data/v1", AddressingStyle: "path", Credentials: appconfig.WorkerStorageCredentialConfig{Type: "static", AccessKeyID: "example-access", SecretAccessKey: "example-secret"}}},
	}
	registry, err := storagecredentials.NewRegistry(s.Config.Workers.ObjectStorage)
	if err != nil {
		t.Fatal(err)
	}
	s.storageCredentials = registry
}

func TestBucketAPIAndCloudflareBindings(t *testing.T) {
	s, store, _ := cfTestServer(t)
	enableTestObjectStorage(t, s)
	base := "/api/v1/orgs/org-a/buckets"
	if out := request(s, "POST", base, "krun_cli_viewer", `{"name":"images"}`); out.Code != 403 {
		t.Fatalf("viewer created bucket: %d %s", out.Code, out.Body.String())
	}
	created := request(s, "POST", base, "krun_cli_admin", `{"name":"images"}`)
	if created.Code != 201 {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	if strings.Contains(created.Body.String(), "store_id") || strings.Contains(created.Body.String(), "example-secret") {
		t.Fatal("physical storage configuration leaked")
	}
	var bucket domain.WorkerBucket
	if err := json.Unmarshal(created.Body.Bytes(), &bucket); err != nil {
		t.Fatal(err)
	}
	if out := request(s, "POST", base, "krun_cli_admin", `{"name":"images"}`); out.Code != 409 {
		t.Fatal("duplicate bucket name accepted")
	}
	cf := cfPrefix + "/accounts/org-a/r2/buckets/images"
	if out := request(s, "GET", cf, "krun_cli_admin", ""); out.Code != 200 {
		t.Fatal(out.Code, out.Body.String())
	}
	if out := request(s, "GET", strings.Replace(cf, "org-a", "org-b", 1), "krun_cli_admin", ""); out.Code != 404 {
		t.Fatal("cross-account bucket visible", out.Code)
	}
	input, err := cfBindingInput(cfUploadMetadata{Bindings: []cfBinding{{Name: "IMAGES", Type: "r2_bucket", BucketName: "images"}}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.View(context.Background(), PartitionAll, func(st *State) error {
		app := s.workerApplication(nil)
		app.Require = func(*State, Principal, string, Target) error { return nil }
		if err := app.StageBindings(t.Context(), st, Principal{}, workerapp.WorkerRef{Organization: "org-a", Worker: "svc-a", Environment: "production"}, input); err != nil {
			return err
		}
		env := st.Workers.Workers["svc-a"].Environments["production"]
		version := workerapp.SnapshotVersion("org-a", "svc-a", "production", WorkerArtifact{}, env)
		if version.R2Bindings["IMAGES"].BucketID != bucket.ID || version.R2Bindings["IMAGES"].StoreID != "primary" {
			t.Fatal("binding not resolved")
		}
		env.R2Bindings["IMAGES"] = domain.WorkerR2Binding{BucketID: "changed", BucketName: "changed"}
		if version.R2Bindings["IMAGES"].BucketID != bucket.ID {
			t.Fatal("version binding changed with desired configuration")
		}
		encoded, _ := json.Marshal(cfBindings(WorkerEnvironment{R2Bindings: version.R2Bindings}))
		if !strings.Contains(string(encoded), `"bucket_name":"images"`) || strings.Contains(string(encoded), "store_id") {
			t.Fatal("invalid Cloudflare binding response")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestStorageCredentialAuthorizationAndDisable(t *testing.T) {
	s, store, _ := cfTestServer(t)
	enableTestObjectStorage(t, s)
	if err := store.Update(context.Background(), PartitionWorkers, func(st *State) error {
		st.Workers.Buckets["bucket-test"] = domain.WorkerBucket{ID: "bucket-test", Organization: "org-a", Name: "images", Store: "primary", Enabled: true, Revision: 1, Created: now()}
		service := st.Workers.Workers["svc-a"]
		env := service.Environments["production"]
		env.Enabled = true
		env.Version = "version-test"
		service.Environments["production"] = env
		st.Workers.Workers["svc-a"] = service
		st.Workers.Artifacts["artifact-test"] = WorkerArtifact{ID: "artifact-test", Organization: "org-a", State: "finalized", Created: now()}
		poolID := s.Config.Workers.RuntimePools[0].ID
		st.Workers.Placements[workerPlacementKey("svc-a", poolID)] = WorkerPlacement{Worker: "svc-a", Organization: "org-a", Pool: poolID, Role: "primary", State: "active", Revision: 1, Created: now(), Updated: now()}
		st.Workers.Versions["version-test"] = WorkerVersion{Artifact: "artifact-test", Created: now(), ID: "version-test", Organization: "org-a", Worker: "svc-a", Environment: "production", R2Bindings: map[string]domain.WorkerR2Binding{"IMAGES": {BucketID: "bucket-test", BucketName: "images", StoreID: "primary"}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	path := "/api/internal/workers/versions/version-test/bindings/IMAGES/credentials"
	pool := s.Config.Workers.RuntimePools[0]
	public := request(s, "POST", path, "krun_cli_admin", "{}")
	if public.Code != 404 {
		t.Fatal("credential endpoint is exposed on the public listener")
	}
	validatorRequest := httptest.NewRequest(http.MethodGet, "/api/internal/workers/config/snapshot", nil)
	validatorRequest.Header.Set("Authorization", "Bearer "+pool.ValidatorToken)
	validatorRequest.Header.Set("X-Runtime-Pool", pool.ID)
	validatorResponse := httptest.NewRecorder()
	s.serveInternal(validatorResponse, validatorRequest)
	if validatorResponse.Code != 200 {
		t.Fatal("validator identity was not recognized for validation reads")
	}
	call := func(path, token, pool string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Runtime-Pool", pool)
		out := httptest.NewRecorder()
		s.serveInternal(out, req)
		return out
	}
	for _, test := range []struct {
		path, token, pool string
		status            int
	}{
		{path, pool.RuntimeToken, pool.ID, 200},
		{path, pool.ValidatorToken, pool.ID, 403},
		{path, pool.RuntimeToken, "other-pool", 403},
		{strings.Replace(path, "IMAGES", "OTHER", 1), pool.RuntimeToken, pool.ID, 404},
		{strings.Replace(path, "version-test", "version-other", 1), pool.RuntimeToken, pool.ID, 404},
	} {
		out := call(test.path, test.token, test.pool)
		if out.Code != test.status {
			t.Fatalf("credential authorization: %d %s", out.Code, out.Body.String())
		}
		if out.Code == 200 {
			var grant map[string]any
			_ = json.Unmarshal(out.Body.Bytes(), &grant)
			if grant["prefix"] != "worker-data/v1/org/org-a/buckets/bucket-test/" || grant["secret_access_key"] != "example-secret" || out.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("incorrect internal grant")
			}
		}
	}
	out := request(s, "PATCH", "/api/v1/orgs/org-a/buckets/bucket-test", "krun_cli_admin", `{"enabled":false,"revision":1}`)
	if out.Code != 200 {
		t.Fatal(out.Code, out.Body.String())
	}
	if out := call(path, pool.RuntimeToken, pool.ID); out.Code != 404 {
		t.Fatal("disabled bucket reused cached credentials", out.Code)
	}
}
