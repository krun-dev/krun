package control

import (
	"bytes"
	"context"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/krun-dev/krun/internal/adapters/objectstore"
	"github.com/krun-dev/krun/internal/adapters/runtimeclient"
	"github.com/krun-dev/krun/internal/adapters/storagecredentials"
	workerartifact "github.com/krun-dev/krun/internal/artifact"
	"github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/securetoken"
	workerapp "github.com/krun-dev/krun/internal/workers"
)

var runtimeStorageConfig = flag.String("runtime-storage-config", "", "Main YAML for opt-in real STS/workerd asset and artifact tests")

// Metadata uses an isolated PostgreSQL test schema; authorization handlers, the rendered
// serving/validator runtimes, STS sessions and object storage are real.
func TestConfiguredRuntimeReads(t *testing.T) {
	if *runtimeStorageConfig == "" {
		t.Skip("provide -runtime-storage-config for real STS and S3 reads")
	}
	if os.Getenv("KRUN_TEST_POSTGRES_URL") == "" {
		t.Fatal("set KRUN_TEST_POSTGRES_URL to a dedicated PostgreSQL test database")
	}
	cfg, err := config.LoadConfig(*runtimeStorageConfig)
	if err != nil || cfg.Workers == nil || cfg.Workers.S3RuntimeCredentials == nil || cfg.Workers.S3RuntimeCredentials.Type != "tos_assume_role" {
		t.Fatal("main configuration must explicitly select runtime STS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	s, store, _ := workerTestServer(t)
	topology := s.Config.Workers
	w := *cfg.Workers
	w.RuntimeAPIURL = ""
	w.ObjectStorage = nil
	w.RuntimePools = topology.RuntimePools
	w.NetworkProfiles = topology.NetworkProfiles
	w.Gateways = topology.Gateways
	w.Domains = topology.Domains
	w.RuntimePools[0].RuntimeToken = securetoken.Random()
	w.RuntimePools[0].ManagementToken = securetoken.Random()
	w.Domains[0].StaticDomain = "static.fixture.test"
	w.S3Prefix += "/integration-tests/" + securetoken.NewID("reads")
	w.ConfigBaseURL = w.S3ObjectURL(w.S3RuntimeEndpoint, "config")
	s.Config.Workers = &w
	s.routes()
	html := []byte("<!doctype html><title>live STS assets</title>")
	script := []byte(`export default {
  async fetch(request, env) {
    return Response.json({
      ok: true,
      leaked: Boolean(env.storage || env.secret_access_key || env.session_token || env.STORAGE_SECRET_KEY),
    });
  },
};`)
	assets := &workerartifact.WorkerManifestAssets{
		Directory: "public", HTMLHandling: "auto-trailing-slash", NotFoundHandling: "single-page-application", RunWorkerFirst: []string{"/api/*"},
		Files: []workerartifact.WorkerManifestFile{
			{Path: "index.html", Digest: workerartifact.BytesDigest(html), Size: len(html), ContentType: "text/html; charset=utf-8"},
			{Path: "about.html", Digest: workerartifact.BytesDigest(html), Size: len(html), ContentType: "text/html; charset=utf-8"},
		},
	}
	archive, err := workerartifact.PackModules("index.js", "2026-09-28", nil, []workerartifact.UploadedModule{{Name: "index.js", Type: "esm", Data: script}}, assets, map[string][]byte{"index.html": html, "about.html": html})
	if err != nil {
		t.Fatal(err)
	}
	code, manifestDigest, err := workerartifact.ParseWorkerArchive(archive)
	if err != nil {
		t.Fatal(err)
	}
	at := now()
	artifact := WorkerArtifact{
		ID: "art-live-reads", Organization: "org-a", State: "finalized", Profile: code.Profile,
		StorageLocation: w.S3LocationID, Bucket: w.S3Bucket,
		ObjectKey:     "v1/org-a/sha256/" + code.ArchiveDigest + "/bundle.tar.gz",
		ArchiveDigest: code.ArchiveDigest, ManifestDigest: manifestDigest, ModuleDigest: code.Digest,
		CompatibilityDate: code.CompatibilityDate, CompatibilityFlags: code.CompatibilityFlags,
		Size: len(archive), Created: at,
	}
	version := WorkerVersion{
		ID: "rel-live-reads", Organization: artifact.Organization, Worker: "svc-a", Environment: "production",
		Artifact: artifact.ID, Profile: artifact.Profile, ArchiveDigest: artifact.ArchiveDigest,
		ModuleDigest: artifact.ModuleDigest, CompatibilityDate: artifact.CompatibilityDate,
		CompatibilityFlags: artifact.CompatibilityFlags, Created: at,
	}
	pool := w.RuntimePools[0].ID
	if err := store.Update(ctx, PartitionWorkers, func(st *State) error {
		st.Workers.Versions[version.ID] = version
		st.Workers.Artifacts[artifact.ID] = artifact

		worker := st.Workers.Workers[version.Worker]
		env := worker.Environments[version.Environment]
		env.Version, env.Confirmed = version.ID, version.ID
		worker.Environments[version.Environment] = env
		st.Workers.Workers[worker.Worker] = worker

		st.Workers.Placements[workerPlacementKey(worker.Worker, pool)] = WorkerPlacement{
			Worker: worker.Worker, Organization: worker.Organization, Pool: pool, NetworkProfile: worker.NetworkProfile,
			Role: "primary", State: "active", Revision: 1, Created: at, Updated: at,
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, stored, err := store.RuntimeVersion(ctx, version.ID, pool); err != nil || stored.ObjectKey != artifact.ObjectKey {
		t.Fatalf("fixture version is unavailable to runtime pool %s: %v", pool, err)
	}

	manager, err := storagecredentials.NewArtifactManager(&w)
	if err != nil {
		t.Fatal("runtime STS manager unavailable")
	}
	client, err := objectstore.NewWorkerS3(&w)
	if err != nil {
		t.Fatal("fixture writer unavailable")
	}
	controller := &workerapp.Controller{Service: s.workerApplication(nil), Config: &w, S3: client, PutObject: client.PutObject, ReadCredentials: manager}
	s.workers = controller
	objects := map[string][]byte{artifact.ObjectKey: archive, workerapp.WorkerArtifactManifestKey(artifact): code.ManifestRaw}
	metadata := map[string]objectstore.PutObjectOptions{}
	for _, prefix := range []string{"int", "pub"} {
		for _, name := range []string{"index.html", "about.html"} {
			key := workerapp.WorkerAssetObjectKey(prefix, version.Organization, version.Worker, code.ArchiveDigest, name)
			objects[key] = html
			metadata[key] = objectstore.PutObjectOptions{ContentType: "text/html; charset=utf-8", UserMetadata: map[string]string{"krun-sha256": workerartifact.BytesDigest(html)}}
		}
	}
	position, err := store.ConfigPosition(ctx)
	if err != nil {
		t.Fatal("fixture config cursor unavailable")
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 45*time.Second)
		defer stop()
		for key := range objects {
			if err := client.RemoveObject(cleanup, w.S3Bucket, key, objectstore.RemoveObjectOptions{}); err != nil {
				t.Error("fixture cleanup failed")
			}
			if _, err := client.StatObject(cleanup, w.S3Bucket, key, objectstore.StatObjectOptions{}); objectstore.ToErrorResponse(err).StatusCode != 404 {
				t.Error("fixture absence could not be confirmed")
			}
		}
	})
	for key, data := range objects {
		if _, err := client.PutObject(ctx, w.S3Bucket, key, bytes.NewReader(data), int64(len(data)), metadata[key]); err != nil {
			t.Fatal("fixture upload failed:", objectstore.ToErrorResponse(err).Code)
		}
	}
	var manifests, renewals, staticGrants, bootstraps atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, request *http.Request) {
		path := request.URL.Path
		if strings.HasSuffix(path, "/config/snapshot") {
			bootstraps.Add(1)
		}
		if strings.Contains(path, "/config/routes/") {
			if _, allowed := s.workerRuntimeServingPool(request); !allowed {
				out.WriteHeader(403)
				return
			}
			s.json(out, 200, map[string]any{"pool": "default", "config_epoch": position.Epoch, "config_revision": position.Revision, "routes": []map[string]any{{"host": "live.fixture.test", "version": version.ID, "enabled": true}}, "versions": map[string]any{version.ID: version}})
			return
		}
		if strings.HasSuffix(path, "/assets") {
			manifests.Add(1)
		}
		if strings.Contains(path, "/versions/") && strings.HasSuffix(path, "/assets/credentials") {
			renewals.Add(1)
		}
		if strings.Contains(path, "/static-assets/") {
			staticGrants.Add(1)
		}
		s.serveInternal(out, request)
	}))
	defer backend.Close()
	runtimeURL, management := startLiveReadRuntime(t, ctx, &w, backend.Listener.Addr().String(), false)
	call := func(method, path, host string, headers map[string]string) (int, http.Header, []byte) {
		t.Helper()
		request, _ := http.NewRequestWithContext(ctx, method, runtimeURL+path, nil)
		request.Host = host
		for key, value := range headers {
			request.Header.Set(key, value)
		}
		response, err := (&http.Client{Timeout: 25 * time.Second}).Do(request)
		if err != nil {
			t.Fatal("runtime request failed")
		}
		defer response.Body.Close()
		data, err := io.ReadAll(io.LimitReader(response.Body, 32768))
		if err != nil {
			t.Fatal("runtime response failed")
		}
		return response.StatusCode, response.Header, data
	}
	status, headers, body := call("GET", "/", "live.fixture.test", nil)
	if status != 200 || !bytes.Equal(body, html) {
		t.Fatalf("live asset request returned %d", status)
	}
	if status, _, body := call("GET", "/about", "live.fixture.test", nil); status != 200 || !bytes.Equal(body, html) {
		t.Fatal("second asset failed")
	}
	if status, _, _ := call("GET", "/", "live.fixture.test", map[string]string{"If-None-Match": headers.Get("ETag")}); status != 304 {
		t.Fatal("asset revalidation failed")
	}
	if status, _, body := call("HEAD", "/about", "live.fixture.test", nil); status != 200 || len(body) != 0 {
		t.Fatal("asset HEAD failed")
	}
	if bootstraps.Load() != 1 {
		t.Fatal("Runtime did not bootstrap exactly once from the control plane")
	}
	if manifests.Load() != 1 || renewals.Load() != 0 {
		t.Fatal("asset reads made per-file control calls")
	}
	if status, _, body := call("GET", "/api/check", "live.fixture.test", nil); status != 200 || !bytes.Contains(body, []byte(`"ok":true`)) || !bytes.Contains(body, []byte(`"leaked":false`)) {
		t.Fatalf("live artifact execution failed: %d", status)
	}
	staticPath := "/" + version.Organization + "/" + version.Worker + "/" + version.ArchiveDigest + "/"
	for _, name := range []string{"index.html", "about.html", "index.html"} {
		if status, _, body := call("GET", staticPath+name, "static.fixture.test", nil); status != 200 || !bytes.Equal(body, html) {
			t.Fatal("signed static origin failed")
		}
	}
	if staticGrants.Load() != 1 {
		t.Fatal("static origin did not reuse its directory grant")
	}
	// Verify the real STS policy cannot write, list, or read an adjacent scope.
	grant, err := controller.ArtifactReadGrant(ctx, artifact)
	if err != nil || grant.Credentials.SessionToken == "" {
		t.Fatal("artifact did not use temporary credentials")
	}
	verifyLiveReadRestrictions(t, ctx, &w, grant, artifact.ObjectKey)
	payload := map[string]any{"version": version, "artifact": map[string]any{"object_key": "bundle.tar.gz", "size": artifact.Size, "archive_sha256": artifact.ArchiveDigest, "storage": runtimeclient.ReadGrantPayload(grant)}}
	validateLiveReadCandidate(t, ctx, management, w.RuntimePools[0].ManagementToken, payload)
	_, validator := startLiveReadRuntime(t, ctx, &w, backend.Listener.Addr().String(), true)
	validateLiveReadCandidate(t, ctx, validator, w.RuntimePools[0].ManagementToken, payload)
	t.Log("PASS real STS scopes, control-plane config bootstrap, S3-signed assets/ETag/HEAD, cached directory grants, private static origin, artifact execution, credential isolation, serving and dedicated validation")
}
