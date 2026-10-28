package control

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/minio/minio-go/v7"
)

func workerConfigFixture() (*State, *Store) {
	store := memoryStore()
	st, _, _ := fixture()
	// Hosts are derived from the domain catalog, not stored.
	ws := WorkerService{Service: "svc-a", Organization: "org-a", Domains: []string{"internal"}, Environments: map[string]WorkerEnvironment{
		"production": {Name: "production", Enabled: true, Release: "rel-1"},
		"preview":    {Name: "preview", Enabled: false, Release: "rel-1"},
		"empty":      {Name: "empty", Enabled: true},
	}}
	st.Workers.Services["svc-a"] = ws
	st.Workers.Releases["rel-1"] = WorkerRelease{ID: "rel-1", Service: "svc-a", Organization: "org-a", Environment: "production", Artifact: "art-1", ArchiveDigest: "archive-1", ModuleDigest: "module-1", Pool: "default", Variables: map[string]string{"A": "1"}}
	st.Workers.Releases["rel-unused"] = WorkerRelease{ID: "rel-unused", Service: "svc-a", Organization: "org-a", Environment: "production", Artifact: "art-1", Pool: "default"}
	st.Workers.Services["svc-deleted"] = WorkerService{Service: "svc-deleted", Organization: "org-a", Environments: map[string]WorkerEnvironment{}}
	_ = store.Update(context.Background(), PartitionAll, func(state *State) error { *state = *st; return nil })
	return st, store
}

func TestWorkerConfigProjection(t *testing.T) {
	st, _ := workerConfigFixture()
	p := workerConfigFromState(st, []WorkerDomain{{ID: "internal", Domain: "workers.test"}})
	if len(p.Routes) != 3 {
		t.Fatalf("routes: %+v", p.Routes)
	}
	slug := workerSlug("org-a", "svc-a")
	wantHosts := []string{hostFor(slug, "empty", "workers.test"), hostFor(slug, "preview", "workers.test"), hostFor(slug, "production", "workers.test")}
	sort.Strings(wantHosts)
	for i, want := range wantHosts {
		if p.Routes[i].Host != want {
			t.Fatalf("route %d host = %q, want %q (routes: %+v)", i, p.Routes[i].Host, want, p.Routes)
		}
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
	if again, _, _ := workerConfigDigest(workerConfigFromState(st, []WorkerDomain{{ID: "internal", Domain: "workers.test"}})); again != digest {
		t.Fatal("projection digest is not deterministic")
	}
}

func TestWorkerConfigDiffOrdering(t *testing.T) {
	st, _ := workerConfigFixture()
	first := workerConfigFromState(st, []WorkerDomain{{ID: "internal", Domain: "workers.test"}})
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
	prodHost := hostFor(workerSlug("org-a", "svc-a"), "production", "workers.test")
	for i := range next.Routes {
		if next.Routes[i].Host == prodHost {
			next.Routes[i].Release = "rel-2"
		}
	}
	next.Releases["rel-2"] = WorkerRelease{ID: "rel-2", Service: "svc-a", Organization: "org-a", Environment: "production", Artifact: "art-2", Pool: "default"}
	ops = diffWorkerConfig(&first, next)
	if len(ops) != 2 || ops[0].Type != "release_put" || ops[0].Release.ID != "rel-2" || ops[1].Type != "route_upsert" || ops[1].Host != prodHost {
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

func TestConfigRevisionCASRejectsStalePublisher(t *testing.T) {
	store := memoryStore()
	ctx := context.Background()
	revision, _, appended, err := store.AppendConfigRevisionCAS(ctx, 0, "first", []byte(`{"routes":[]}`), []byte(`[]`), 10)
	if err != nil || !appended || revision != 1 {
		t.Fatalf("first CAS: revision=%d appended=%v err=%v", revision, appended, err)
	}
	revision, _, appended, err = store.AppendConfigRevisionCAS(ctx, 0, "stale", []byte(`{"routes":[]}`), []byte(`[]`), 10)
	if err != nil || appended || revision != 1 {
		t.Fatalf("stale CAS: revision=%d appended=%v err=%v", revision, appended, err)
	}
	items, _, err := store.ConfigRevisionsSince(ctx, 0, 10)
	if err != nil || len(items) != 1 || items[0].Digest != "first" {
		t.Fatalf("stale publisher created a duplicate revision: %+v err=%v", items, err)
	}
}

func TestLoadConfigFailsClosedOnDigestMismatch(t *testing.T) {
	s, store := testServer(t)
	projection := workerConfigProjection{
		Routes:   []workerConfigRoute{{Host: "stale.workers.test", Path: "/", Enabled: true}},
		Releases: map[string]WorkerRelease{},
	}
	_, payload, err := workerConfigDigest(projection)
	if err != nil {
		t.Fatal(err)
	}
	if revision, _, appended, err := store.AppendConfigRevisionCAS(context.Background(), 0, "corrupt-digest", payload, []byte(`[]`), 10); err != nil || !appended || revision != 1 {
		t.Fatalf("seed corrupt head: revision=%d appended=%v err=%v", revision, appended, err)
	}
	controller := &workersController{server: s, config: s.Config.Workers}
	for attempt := 0; attempt < 2; attempt++ {
		if err := controller.loadConfigLocked(context.Background()); err == nil {
			t.Fatalf("attempt %d accepted a normalized head whose digest does not match", attempt+1)
		}
		if controller.configLoaded || controller.configLast != nil {
			t.Fatalf("attempt %d left corrupt head loaded: loaded=%v projection=%+v", attempt+1, controller.configLoaded, controller.configLast)
		}
	}
}

func TestPublishConfigCASDoesNotReturnStaleWinner(t *testing.T) {
	s, store := testServer(t)
	s.Config.Workers = testWorkersConfig()
	st, _, _ := fixture()
	service := WorkerService{Service: "svc-a", Organization: "org-a", Domains: []string{"internal"}, Environments: map[string]WorkerEnvironment{
		"production": {Name: "production", Enabled: true, Release: "rel-fresh"},
	}}
	st.Workers.Services[service.Service] = service
	st.Workers.Releases["rel-fresh"] = WorkerRelease{ID: "rel-fresh", Service: service.Service, Organization: service.Organization, Environment: "production", Artifact: "art-fresh", Pool: "default"}
	if err := store.Update(context.Background(), PartitionAll, func(current *State) error { *current = *st; return nil }); err != nil {
		t.Fatal(err)
	}

	// Simulate another replica winning revision 1 from a projection it read
	// before rel-fresh committed.
	stale := workerConfigProjection{Routes: []workerConfigRoute{}, Releases: map[string]WorkerRelease{}}
	staleDigest, stalePayload, err := workerConfigDigest(stale)
	if err != nil {
		t.Fatal(err)
	}
	staleOps, _ := json.Marshal(diffWorkerConfig(nil, stale))
	if revision, _, appended, err := store.AppendConfigRevisionCAS(context.Background(), 0, staleDigest, stalePayload, staleOps, workerConfigOpsRetain); err != nil || !appended || revision != 1 {
		t.Fatalf("seed stale winner: revision=%d appended=%v err=%v", revision, appended, err)
	}

	controller := &workersController{server: s, config: s.Config.Workers, configLoaded: true, configRevision: 0, assetCache: map[string]workerCachedAssets{}}
	controller.putObject = func(_ context.Context, _, _ string, reader io.Reader, _ int64, _ minio.PutObjectOptions) (minio.UploadInfo, error) {
		_, err := io.ReadAll(reader)
		return minio.UploadInfo{}, err
	}
	revision, err := controller.publishConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if revision != 2 {
		t.Fatalf("publish returned stale revision %d, want fresh revision 2", revision)
	}
	latest, ok, err := store.LatestConfigRevision(context.Background())
	if err != nil || !ok || latest.Revision != revision {
		t.Fatalf("latest=%+v ok=%v err=%v", latest, ok, err)
	}
	var projection workerConfigProjection
	if err := json.Unmarshal(latest.Payload, &projection); err != nil {
		t.Fatal(err)
	}
	if _, ok := projection.Releases["rel-fresh"]; !ok {
		t.Fatalf("returned revision does not contain fresh release: %+v", projection)
	}
}

func TestPublishConfigCreatesRevisionsAndObjects(t *testing.T) {
	s, store := testServer(t)
	s.Config.Workers = testWorkersConfig()
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
	ws := WorkerService{Service: "svc-a", Organization: "org-a", Domains: []string{"internal"}, Environments: map[string]WorkerEnvironment{"production": {Name: "production", Enabled: true, Release: "rel-1"}}}
	st.Workers.Services["svc-a"] = ws
	st.Workers.Releases["rel-1"] = WorkerRelease{ID: "rel-1", Service: "svc-a", Organization: "org-a", Environment: "production", Artifact: "art-1", Pool: "default"}
	if err := store.Update(context.Background(), PartitionAll, func(state *State) error { *state = *st; return nil }); err != nil {
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
	prodHost := hostFor(workerSlug("org-a", "svc-a"), "production", "workers.test")
	if snapshot.ConfigRevision != 1 || len(snapshot.Routes) != 1 || snapshot.Routes[0].Host != prodHost || snapshot.Releases["rel-1"].Artifact != "art-1" {
		t.Fatalf("snapshot payload: %+v", snapshot)
	}
	if _, ok := objects[workerConfigLatestKey]; !ok {
		t.Fatal("latest pointer missing")
	}
	before := len(objects)
	if again, err := controller.publishConfig(context.Background()); err != nil || again != 1 || len(objects) != before {
		t.Fatalf("unchanged projection must not republish: %d %v objects=%d", again, err, len(objects))
	}
	if err := store.Update(context.Background(), PartitionAll, func(state *State) error {
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
	s.Config.Workers = func() *WorkersConfig { c := testWorkersConfig(); c.RuntimeToken = token; return c }()
	s.routes()
	ctx := context.Background()
	payload, _ := json.Marshal(workerConfigProjection{Routes: []workerConfigRoute{{Host: "w.workers.test", Enabled: true, Release: "rel-1", Pool: "default"}}, Releases: map[string]WorkerRelease{}})
	ops, _ := json.Marshal([]workerConfigOp{{Type: "route_upsert", Pool: "default", Host: "w.workers.test"}})
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

func TestMultiPoolRuntimeConfigIsolation(t *testing.T) {
	s, store := testServer(t)
	c := testWorkersConfig()
	c.RuntimePools = []WorkerRuntimePool{
		{ID: "private-a", Access: []string{"private"}, RuntimeService: "runtime-private", RuntimeToken: strings.Repeat("p", 24), ManagementToken: strings.Repeat("q", 24), Default: true},
		{ID: "public-b", Access: []string{"public"}, RuntimeService: "runtime-public", RuntimeToken: strings.Repeat("u", 24), ManagementToken: strings.Repeat("v", 24)},
	}
	s.Config.Workers = c
	s.routes()
	ctx := context.Background()
	projection := workerConfigProjection{
		Routes: []workerConfigRoute{
			{Host: "private.workers.test", Enabled: true, Release: "rel-private", Pool: "private-a"},
			{Host: "public.workers.test", Enabled: true, Release: "rel-public", Pool: "public-b"},
		},
		Releases: map[string]WorkerRelease{
			"rel-private": {ID: "rel-private", Pool: "private-a"},
			"rel-public":  {ID: "rel-public", Pool: "public-b"},
		},
	}
	payload, _ := json.Marshal(projection)
	if _, _, err := store.AppendConfigRevision(ctx, "d1", payload, []byte(`[]`), 512); err != nil {
		t.Fatal(err)
	}
	ops, _ := json.Marshal([]workerConfigOp{
		{Type: "route_upsert", Pool: "private-a", Host: "private.workers.test", Route: &projection.Routes[0]},
		{Type: "route_upsert", Pool: "public-b", Host: "public.workers.test", Route: &projection.Routes[1]},
	})
	if _, _, err := store.AppendConfigRevision(ctx, "d2", payload, ops, 512); err != nil {
		t.Fatal(err)
	}

	requestPool := func(pool, token, path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/api/internal/workers/config"+path, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("X-Runtime-Pool", pool)
		w := httptest.NewRecorder()
		s.serveInternal(w, r)
		return w
	}
	privateSnapshot := requestPool("private-a", strings.Repeat("p", 24), "/snapshot")
	if privateSnapshot.Code != http.StatusOK || !strings.Contains(privateSnapshot.Body.String(), "private.workers.test") || strings.Contains(privateSnapshot.Body.String(), "public.workers.test") {
		t.Fatalf("private snapshot isolation: %d %s", privateSnapshot.Code, privateSnapshot.Body.String())
	}
	privateChanges := requestPool("private-a", strings.Repeat("p", 24), "/changes?since=1&wait=1s")
	if privateChanges.Code != http.StatusOK || !strings.Contains(privateChanges.Body.String(), "private.workers.test") || strings.Contains(privateChanges.Body.String(), "public.workers.test") {
		t.Fatalf("private changes isolation: %d %s", privateChanges.Code, privateChanges.Body.String())
	}
	wrongCredential := requestPool("private-a", strings.Repeat("u", 24), "/snapshot")
	if wrongCredential.Code != http.StatusForbidden {
		t.Fatalf("cross-pool credential accepted: %d %s", wrongCredential.Code, wrongCredential.Body.String())
	}
}
