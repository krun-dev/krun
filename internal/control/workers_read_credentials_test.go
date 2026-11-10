package control

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	workerartifact "github.com/krun-dev/krun/internal/artifact"
	appconfig "github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/domain"
	workerapp "github.com/krun-dev/krun/internal/workers"
)

type readCredentialRecorder struct {
	scopes []domain.StorageCredentialScope
	fail   bool
}

func (r *readCredentialRecorder) Credentials(_ context.Context, scope domain.StorageCredentialScope, minimum time.Duration) (domain.StorageCredentials, error) {
	r.scopes = append(r.scopes, scope)
	if r.fail {
		return domain.StorageCredentials{}, errors.New("issuer unavailable")
	}
	return domain.StorageCredentials{
		AccessKeyID: "session-access", SecretAccessKey: "session-secret",
		SessionToken: "session-token", ExpiresAt: time.Now().Add(time.Hour),
	}, nil
}

func TestRuntimeReadCredentialsScopeAndTransport(t *testing.T) {
	s, store, _ := workerTestServer(t)
	version := seedPublishedWorker(t, store)
	c := s.Config.Workers
	c.S3Prefix = "installation/dev"
	c.S3RuntimeEndpoint = "https://storage.example.com"
	c.S3Region = "us-east-1"
	c.S3LocationID = "primary"
	recorder := &readCredentialRecorder{}
	s.workers = &workerapp.Controller{
		Service: s.workerApplication(nil), Config: c, ReadCredentials: recorder,
		AssetCache: map[string]workerapp.WorkerCachedAssets{
			version.Artifact: {
				Config: &domain.WorkerAssetsConfig{Directory: "public"},
				Files:  []workerartifact.WorkerManifestFile{{Path: "index.html", Digest: strings.Repeat("c", 64), Size: 10}},
			},
		},
	}
	if err := store.Update(context.Background(), PartitionWorkers, func(st *State) error {
		artifact := st.Workers.Artifacts[version.Artifact]
		artifact.StorageLocation = c.S3LocationID
		artifact.Bucket = c.S3Bucket
		artifact.ObjectKey = "v1/org-a/sha256/" + artifact.ArchiveDigest + "/bundle.tar.gz"
		st.Workers.Artifacts[artifact.ID] = artifact
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	pool := c.DefaultRuntimePool()
	invoke := func(method, path, token, poolID string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "/api/internal/workers"+path, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("X-Runtime-Pool", poolID)
		out := httptest.NewRecorder()
		s.serveInternal(out, r)
		return out
	}
	cases := []struct{ method, path, prefix string }{
		{http.MethodGet, "/artifacts/" + version.Artifact, "v1/org-a/sha256/" + version.ArchiveDigest + "/"},
		{http.MethodGet, "/versions/" + version.ID + "/assets", "int/org-a/svc-a/" + version.ArchiveDigest + "/"},
		{http.MethodPost, "/versions/" + version.ID + "/assets/credentials", "int/org-a/svc-a/" + version.ArchiveDigest + "/"},
		{http.MethodPost, "/static-assets/int/org-a/svc-a/" + version.ArchiveDigest + "/credentials", "int/org-a/svc-a/" + version.ArchiveDigest + "/"},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			out := invoke(tc.method, tc.path, pool.RuntimeToken, pool.ID)
			if out.Code != http.StatusOK {
				t.Fatalf("status %d: %s", out.Code, out.Body.String())
			}
			if out.Header().Get("Cache-Control") != "no-store" || strings.Contains(out.Body.String(), "download_url") {
				t.Fatal("read grant was cacheable or retained presigned URL transport")
			}
			var payload map[string]any
			if err := json.Unmarshal(out.Body.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			if nested, ok := payload["storage"].(map[string]any); ok {
				payload = nested
			}
			if payload["session_token"] != "session-token" || payload["secret_access_key"] != "session-secret" {
				t.Fatal("trusted transport omitted session credentials")
			}
			scope := recorder.scopes[len(recorder.scopes)-1]
			if scope.Access != domain.StorageRead || scope.Organization != "org-a" ||
				scope.Bucket != c.S3Bucket || scope.Prefix != "installation/dev/"+tc.prefix {
				t.Fatalf("incorrect read scope: %#v", scope)
			}
			before := len(recorder.scopes)
			if out := invoke(tc.method, tc.path, "wrong-token", pool.ID); out.Code != http.StatusForbidden {
				t.Fatal("unauthenticated read grant accepted")
			}
			if out := invoke(tc.method, tc.path, pool.RuntimeToken, "other-pool"); out.Code != http.StatusForbidden {
				t.Fatal("cross-pool credential accepted")
			}
			c.RuntimePools[0].ValidatorToken = "validator-only-token"
			if out := invoke(tc.method, tc.path, c.RuntimePools[0].ValidatorToken, pool.ID); out.Code != http.StatusForbidden {
				t.Fatal("validator acquired serving credentials")
			}
			if len(recorder.scopes) != before {
				t.Fatal("unauthorized request reached issuer")
			}
		})
	}
	before := len(recorder.scopes)
	if out := invoke(http.MethodPost, "/versions/unknown/assets/credentials", pool.RuntimeToken, pool.ID); out.Code != http.StatusNotFound {
		t.Fatal("unknown Version acquired a grant")
	}
	if out := invoke(http.MethodPost, "/static-assets/pub/org-a/svc-a/"+version.ArchiveDigest+"/credentials", pool.RuntimeToken, pool.ID); out.Code != http.StatusForbidden {
		t.Fatal("private runtime acquired public-origin credentials")
	}
	if len(recorder.scopes) != before {
		t.Fatal("invalid resource reached issuer")
	}
	for _, path := range []string{
		"/versions/" + version.ID + "/assets/index.html",
		"/static-assets/int/org-a/svc-a/" + version.ArchiveDigest + "/index.html",
	} {
		if out := invoke(http.MethodGet, path, pool.RuntimeToken, pool.ID); out.Code != http.StatusNotFound {
			t.Fatal("legacy per-file presigned endpoint is still registered")
		}
	}
	recorder.fail = true
	out := invoke(http.MethodPost, "/versions/"+version.ID+"/assets/credentials", pool.RuntimeToken, pool.ID)
	if out.Code != http.StatusServiceUnavailable || strings.Contains(out.Body.String(), "session-secret") {
		t.Fatal("credential failure did not fail closed")
	}
	raw, err := json.Marshal(domain.StorageReadGrant{Credentials: domain.StorageCredentials{SecretAccessKey: "session-secret"}})
	if err != nil || string(raw) != "{}" {
		t.Fatal("read grant leaked through default JSON serialization")
	}
}

func TestPostgresPublicStaticGrantRequiresPublicRoute(t *testing.T) {
	store := postgresTestStore(t)
	server, _, _ := workerTestServer(t)
	server.Store = store
	cfg := server.Config.Workers
	pool := &cfg.RuntimePools[0]
	pool.Access = []string{"public", "private"}
	cfg.Domains = append(cfg.Domains,
		appconfig.WorkerDomain{Domain: "public.example.test", Access: "public"},
		appconfig.WorkerDomain{Domain: "private.example.test", Access: "private"},
	)
	recorder := &readCredentialRecorder{}
	server.workers = &workerapp.Controller{Config: cfg, ReadCredentials: recorder}
	ctx := t.Context()
	digest := strings.Repeat("a", 64)
	db := postgresTestPool(store)
	_, err := db.Exec(ctx, `
		INSERT INTO krun_worker_versions (version_id, org_id, worker_id, env_name, artifact_id, archive_digest)
		VALUES ('release', 'org-a', 'svc-a', 'production', 'artifact', $1)`, digest)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(ctx, `
		INSERT INTO worker_config_routes (worker_id, shard, host, pool, enabled, version_id)
		VALUES ('svc-a', 'ab', 'svc.private.example.test', $1, true, 'release'),
		       ('svc-a', 'ab', 'svc.public.example.test', $1, false, 'release')`, pool.ID)
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/internal/workers/static-assets/pub/org-a/svc-a/" + digest + "/credentials"
	requestGrant := func() int {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		req.Header.Set("Authorization", "Bearer "+pool.RuntimeToken)
		req.Header.Set("X-Runtime-Pool", pool.ID)
		response := httptest.NewRecorder()
		server.serveInternal(response, req)
		return response.Code
	}

	if status := requestGrant(); status != http.StatusNotFound || len(recorder.scopes) != 0 {
		t.Fatalf("private-only artifact reached the public static origin: %d", status)
	}
	if _, err := db.Exec(ctx, "UPDATE worker_config_routes SET enabled=true WHERE host='svc.public.example.test'"); err != nil {
		t.Fatal(err)
	}
	if status := requestGrant(); status != http.StatusOK || len(recorder.scopes) != 1 {
		t.Fatalf("public artifact did not receive a scoped read grant: %d", status)
	}
}
