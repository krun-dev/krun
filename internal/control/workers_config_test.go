package control

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/minio/minio-go/v7"
)

func workerConfigFixture() (*State, *Store) {
	store := memoryStore()
	st, _, _ := fixture()
	ws := WorkerService{Service: "svc-a", Organization: "org-a", Environments: map[string]WorkerEnvironment{
		"production": {Name: "production", Enabled: true, Host: "w-prod.workers.test", Release: "rel-1"},
		"preview":    {Name: "preview", Enabled: false, Host: "w-prev.workers.test", Release: "rel-1"},
		"empty":      {Name: "empty", Enabled: true, Host: "w-empty.workers.test"},
	}}
	st.Workers.Services["svc-a"] = ws
	st.Workers.Releases["rel-1"] = WorkerRelease{ID: "rel-1", Service: "svc-a", Organization: "org-a", Environment: "production", Artifact: "art-1", ArchiveDigest: "archive-1", ModuleDigest: "module-1", Pool: "default", Variables: map[string]string{"A": "1"}}
	st.Workers.Releases["rel-unused"] = WorkerRelease{ID: "rel-unused", Service: "svc-a", Organization: "org-a", Environment: "production", Artifact: "art-1", Pool: "default"}
	st.Workers.Services["svc-deleted"] = WorkerService{Service: "svc-deleted", Organization: "org-a", Environments: map[string]WorkerEnvironment{}}
	_ = store.Update(context.Background(), func(state *State) error { *state = *st; return nil })
	return st, store
}

func TestWorkerConfigProjection(t *testing.T) {
	st, _ := workerConfigFixture()
	p := workerConfigFromState(st)
	if len(p.Routes) != 3 {
		t.Fatalf("routes: %+v", p.Routes)
	}
	if p.Routes[0].Host != "w-empty.workers.test" || p.Routes[1].Host != "w-prev.workers.test" || p.Routes[2].Host != "w-prod.workers.test" {
		t.Fatalf("routes are not sorted by host: %+v", p.Routes)
	}
	if p.Routes[1].Enabled {
		t.Fatal("disabled environment must stay in the projection with enabled=false")
	}
	if p.Routes[0].Release != "" || p.Routes[2].Release != "rel-1" {
		t.Fatalf("release pointers lost: %+v", p.Routes)
	}
	if _, ok := p.Releases["rel-1"]; !ok || len(p.Releases) != 1 {
		t.Fatalf("only referenced releases should be projected: %+v", p.Releases)
	}
	digest, _, err := workerConfigDigest(p)
	if err != nil || digest == "" {
		t.Fatal(err)
	}
	if again, _, _ := workerConfigDigest(workerConfigFromState(st)); again != digest {
		t.Fatal("projection digest is not deterministic")
	}
}

func TestWorkerConfigDiffOrdering(t *testing.T) {
	st, _ := workerConfigFixture()
	first := workerConfigFromState(st)
	ops := diffWorkerConfig(nil, first)
	seenRoute := false
	for _, op := range ops {
		if op.Type == "route_upsert" {
			seenRoute = true
		}
		if op.Type == "release_put" && seenRoute {
			t.Fatal("release_put must precede the routes that reference it")
		}
	}
	next := first
	next.Routes = append([]workerConfigRoute(nil), first.Routes...)
	next.Releases = map[string]WorkerRelease{"rel-1": first.Releases["rel-1"]}
	for i := range next.Routes {
		if next.Routes[i].Host == "w-prod.workers.test" {
			next.Routes[i].Release = "rel-2"
		}
	}
	next.Releases["rel-2"] = WorkerRelease{ID: "rel-2", Service: "svc-a", Organization: "org-a", Environment: "production", Artifact: "art-2", Pool: "default"}
	ops = diffWorkerConfig(&first, next)
	if len(ops) != 2 || ops[0].Type != "release_put" || ops[0].Release.ID != "rel-2" || ops[1].Type != "route_upsert" || ops[1].Host != "w-prod.workers.test" {
		t.Fatalf("unexpected migration ops: %+v", ops)
	}
	next.Routes = next.Routes[:1]
	ops = diffWorkerConfig(&first, next)
	deleted := 0
	for _, op := range ops {
		if op.Type == "route_delete" {
			deleted++
		}
	}
	if deleted != 2 {
		t.Fatalf("expected two route deletes: %+v", ops)
	}
}

func TestConfigRevisionStoreAndAcks(t *testing.T) {
	store := memoryStore()
	ctx := context.Background()
	for i := 1; i <= 5; i++ {
		revision, floor, err := store.AppendConfigRevision(ctx, "digest-"+string(rune('0'+i)), []byte(`{"routes":[]}`), []byte(`[]`), 3)
		if err != nil {
			t.Fatal(err)
		}
		if int(revision) != i {
			t.Fatalf("revision %d", revision)
		}
		wantFloor := int64(1)
		if i > 3 {
			wantFloor = int64(i - 2)
		}
		if floor != wantFloor {
			t.Fatalf("floor for %d: got %d want %d", i, floor, wantFloor)
		}
	}
	items, floor, err := store.ConfigRevisionsSince(ctx, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if floor != 3 || len(items) != 3 || items[0].Revision != 3 || items[2].Revision != 5 {
		t.Fatalf("since: floor=%d items=%+v", floor, items)
	}
	latest, ok, err := store.LatestConfigRevision(ctx)
	if err != nil || !ok || latest.Revision != 5 {
		t.Fatalf("latest: %+v %v %v", latest, ok, err)
	}
	if err := store.RecordAck(ctx, WorkerAck{Pod: "pod-1", UID: "uid-1", Pool: "default", Revision: 4, Time: now()}); err != nil {
		t.Fatal(err)
	}
	acks, err := store.ReadAcks(ctx)
	if err != nil || acks["uid-1"].Revision != 4 {
		t.Fatalf("acks: %+v %v", acks, err)
	}
}

func TestPublishConfigCreatesRevisionsAndObjects(t *testing.T) {
	s, store := testServer(t)
	s.Config.Workers = &WorkersConfig{Domain: "workers.test", S3Bucket: "bucket"}
	controller := &workersController{server: s, config: s.Config.Workers, assetCache: map[string]workerCachedAssets{}}
	objects := map[string][]byte{}
	controller.putObject = func(ctx context.Context, bucket, key string, reader io.Reader, size int64, opts minio.PutObjectOptions) (minio.UploadInfo, error) {
		raw, err := io.ReadAll(reader)
		if err != nil {
			return minio.UploadInfo{}, err
		}
		objects[key] = raw
		return minio.UploadInfo{}, nil
	}
	st, _, _ := fixture()
	ws := WorkerService{Service: "svc-a", Organization: "org-a", Environments: map[string]WorkerEnvironment{"production": {Name: "production", Enabled: true, Host: "w-prod.workers.test", Release: "rel-1"}}}
	st.Workers.Services["svc-a"] = ws
	st.Workers.Releases["rel-1"] = WorkerRelease{ID: "rel-1", Service: "svc-a", Organization: "org-a", Environment: "production", Artifact: "art-1", Pool: "default"}
	if err := store.Update(context.Background(), func(state *State) error { *state = *st; return nil }); err != nil {
		t.Fatal(err)
	}
	revision, err := controller.publishConfig(context.Background())
	if err != nil || revision != 1 {
		t.Fatalf("first publish: %d %v", revision, err)
	}
	raw, ok := objects[workerConfigSnapshotKey(1)]
	if !ok {
		t.Fatal("snapshot object missing")
	}
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	var snapshot workerConfigSnapshot
	if err := json.NewDecoder(gz).Decode(&snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.ConfigRevision != 1 || len(snapshot.Routes) != 1 || snapshot.Routes[0].Host != "w-prod.workers.test" || snapshot.Releases["rel-1"].Artifact != "art-1" {
		t.Fatalf("snapshot payload: %+v", snapshot)
	}
	if _, ok := objects[workerConfigLatestKey]; !ok {
		t.Fatal("latest pointer missing")
	}
	before := len(objects)
	if again, err := controller.publishConfig(context.Background()); err != nil || again != 1 || len(objects) != before {
		t.Fatalf("unchanged projection must not republish: %d %v objects=%d", again, err, len(objects))
	}
	if err := store.Update(context.Background(), func(state *State) error {
		env := state.Workers.Services["svc-a"].Environments["production"]
		env.Release = "rel-2"
		service := state.Workers.Services["svc-a"]
		service.Environments["production"] = env
		state.Workers.Services["svc-a"] = service
		state.Workers.Releases["rel-2"] = WorkerRelease{ID: "rel-2", Service: "svc-a", Organization: "org-a", Environment: "production", Artifact: "art-2", Pool: "default"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if next, err := controller.publishConfig(context.Background()); err != nil || next != 2 {
		t.Fatalf("second publish: %d %v", next, err)
	}
	var latest workerConfigLatest
	if err := json.Unmarshal(objects[workerConfigLatestKey], &latest); err != nil {
		t.Fatal(err)
	}
	if latest.ConfigRevision != 2 || latest.Snapshot != "snapshots/2.json.gz" || latest.SHA256 == "" {
		t.Fatalf("latest pointer: %+v", latest)
	}
}

func runtimeConfigRequest(s *Server, token, query string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "/api/internal/workers/config"+query, nil)
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("X-Runtime-Pool", "default")
	w := httptest.NewRecorder()
	s.serveInternal(w, r)
	return w
}

func TestWorkerRuntimeConfigChanges(t *testing.T) {
	s, store := testServer(t)
	token := strings.Repeat("t", 32)
	s.Config.Workers = &WorkersConfig{Domain: "workers.test", RuntimeToken: token}
	s.routes()
	ctx := context.Background()
	payload, _ := json.Marshal(workerConfigProjection{Routes: []workerConfigRoute{{Host: "w.workers.test", Enabled: true, Release: "rel-1"}}, Releases: map[string]WorkerRelease{}})
	ops, _ := json.Marshal([]workerConfigOp{{Type: "route_upsert", Host: "w.workers.test"}})
	if _, _, err := store.AppendConfigRevision(ctx, "d1", payload, ops, 512); err != nil {
		t.Fatal(err)
	}
	if r := runtimeConfigRequest(s, token, "/changes?since=0&wait=1s"); r.Code != 409 || !strings.Contains(r.Body.String(), "RESYNC") {
		t.Fatalf("since=0 must resync: %d %s", r.Code, r.Body.String())
	}
	r := runtimeConfigRequest(s, token, "/changes?since=1&wait=1s")
	if r.Code != 204 {
		t.Fatalf("no newer revision: %d %s", r.Code, r.Body.String())
	}
	if _, _, err := store.AppendConfigRevision(ctx, "d2", payload, ops, 512); err != nil {
		t.Fatal(err)
	}
	r = runtimeConfigRequest(s, token, "/changes?since=1&wait=1s")
	if r.Code != 200 {
		t.Fatalf("batch: %d %s", r.Code, r.Body.String())
	}
	var batch struct {
		From int64             `json:"from"`
		To   int64             `json:"to"`
		Ops  []json.RawMessage `json:"ops"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &batch); err != nil || batch.From != 1 || batch.To != 2 || len(batch.Ops) != 1 {
		t.Fatalf("batch: %+v %v", batch, err)
	}
	snapshot := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/internal/workers/config/snapshot", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Runtime-Pool", "default")
	s.serveInternal(snapshot, req)
	if snapshot.Code != 200 || !strings.Contains(snapshot.Body.String(), `"config_revision":2`) {
		t.Fatalf("snapshot: %d %s", snapshot.Code, snapshot.Body.String())
	}
}
