package control

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	appconfig "github.com/krun-dev/krun/internal/config"
	workerapp "github.com/krun-dev/krun/internal/workers"
)

func workerConfigFixture() (*State, *Store) {
	store := memoryStore()
	st, _, _ := fixture()
	// Hosts are derived from the domain catalog, not stored.
	ws := WorkerService{Service: "svc-a", Organization: "org-a", Domains: []string{"internal"}, Environments: map[string]WorkerEnvironment{
		"production": {Name: "production", Enabled: true, Version: "rel-1"},
		"preview":    {Name: "preview", Enabled: false, Version: "rel-1"},
		"empty":      {Name: "empty", Enabled: true},
	}}
	st.Workers.Services["svc-a"] = ws
	placed := now()
	st.Workers.Placements[workerPlacementKey("svc-a", "default")] = WorkerPlacement{Service: "svc-a", Organization: "org-a", NetworkProfile: "default", Pool: "default", Role: "primary", State: "active", Revision: 1, Created: placed, Updated: placed}
	st.Workers.Versions["rel-1"] = WorkerVersion{ID: "rel-1", Service: "svc-a", Organization: "org-a", Environment: "production", Artifact: "art-1", ArchiveDigest: "archive-1", ModuleDigest: "module-1", Variables: map[string]any{"A": "1"}}
	st.Workers.Versions["rel-unused"] = WorkerVersion{ID: "rel-unused", Service: "svc-a", Organization: "org-a", Environment: "production", Artifact: "art-1"}
	st.Workers.Services["svc-deleted"] = WorkerService{Service: "svc-deleted", Organization: "org-a", Environments: map[string]WorkerEnvironment{}}
	_ = store.Update(context.Background(), PartitionAll, func(state *State) error { *state = *st; return nil })
	return st, store
}

func workerProjectionConfig(domains []appconfig.WorkerDomain) *appconfig.WorkersConfig {
	access := []string{}
	privateEgress, publicEgress := "deny", "deny"
	for i := range domains {
		if domains[i].Access == "" {
			domains[i].Access = "public"
		}
		if !contains(access, domains[i].Access) {
			access = append(access, domains[i].Access)
		}
		if domains[i].Access == "private" {
			privateEgress = "direct"
		}
		if domains[i].Access == "public" {
			publicEgress = "direct"
		}
	}
	if len(domains) > 0 {
		domains[0].Default = true
	}
	return &appconfig.WorkersConfig{
		Domains: domains,
		RuntimePools: []appconfig.WorkerRuntimePool{{
			ID: "default", Access: access, PrivateEgress: privateEgress, PublicEgress: publicEgress,
			RuntimeService: "runtime-default", ValidatorService: "runtime-validator-default", Default: true,
		}},
		NetworkProfiles: []appconfig.WorkerNetworkProfile{{
			ID: "default", Name: "Default", Default: true,
			Requirements: appconfig.WorkerNetworkRequirements{Ingress: access, PrivateEgress: privateEgress, PublicEgress: publicEgress},
		}},
	}
}

func TestWorkerConfigProjection(t *testing.T) {
	st, _ := workerConfigFixture()
	config := workerProjectionConfig([]appconfig.WorkerDomain{{ID: "internal", Domain: "workers.test"}})
	p := workerapp.WorkerConfigFromState(st, config)
	if len(p.Routes) != 3 {
		t.Fatalf("routes: %+v", p.Routes)
	}
	slug := appconfig.WorkerSlug("org-a", "svc-a")
	wantHosts := []string{appconfig.HostFor(slug, "empty", "workers.test"), appconfig.HostFor(slug, "preview", "workers.test"), appconfig.HostFor(slug, "production", "workers.test")}
	sort.Strings(wantHosts)
	for i, want := range wantHosts {
		if p.Routes[i].Host != want {
			t.Fatalf("route %d host = %q, want %q (routes: %+v)", i, p.Routes[i].Host, want, p.Routes)
		}
	}
	if p.Routes[1].Enabled {
		t.Fatal("disabled environment must stay in the projection with enabled=false")
	}
	if p.Routes[0].Version != "" || p.Routes[2].Version != "rel-1" {
		t.Fatalf("version pointers lost: %+v", p.Routes)
	}
	if _, ok := p.Versions["rel-1"]; !ok || len(p.Versions) != 1 {
		t.Fatalf("only referenced versions should be projected: %+v", p.Versions)
	}
	digest, _, err := workerapp.WorkerConfigDigest(p)
	if err != nil || digest == "" {
		t.Fatal(err)
	}
	if again, _, _ := workerapp.WorkerConfigDigest(workerapp.WorkerConfigFromState(st, config)); again != digest {
		t.Fatal("projection digest is not deterministic")
	}
}

func TestWorkerConfigDiffOrdering(t *testing.T) {
	st, _ := workerConfigFixture()
	first := workerapp.WorkerConfigFromState(st, workerProjectionConfig([]appconfig.WorkerDomain{{ID: "internal", Domain: "workers.test"}}))
	ops := workerapp.DiffWorkerConfig(nil, first)
	seenRoute := false
	for _, op := range ops {
		if op.Type == "route_upsert" {
			seenRoute = true
		}
		if op.Type == "version_put" && seenRoute {
			t.Fatal("version_put must precede the routes that reference it")
		}
	}
	next := first
	next.Routes = append([]workerConfigRoute(nil), first.Routes...)
	next.Versions = map[string]WorkerVersion{"rel-1": first.Versions["rel-1"]}
	prodHost := appconfig.HostFor(appconfig.WorkerSlug("org-a", "svc-a"), "production", "workers.test")
	for i := range next.Routes {
		if next.Routes[i].Host == prodHost {
			next.Routes[i].Version = "rel-2"
		}
	}
	next.Versions["rel-2"] = WorkerVersion{ID: "rel-2", Service: "svc-a", Organization: "org-a", Environment: "production", Artifact: "art-2"}
	ops = workerapp.DiffWorkerConfig(&first, next)
	if len(ops) != 2 || ops[0].Type != "version_put" || ops[0].Version.ID != "rel-2" || ops[1].Type != "route_upsert" || ops[1].Host != prodHost {
		t.Fatalf("unexpected migration ops: %+v", ops)
	}
	next.Routes = next.Routes[:1]
	ops = workerapp.DiffWorkerConfig(&first, next)
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
	items, floor, head, err := store.ConfigRevisionsSince(ctx, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if head != 5 || floor != 3 || len(items) != 3 || items[0].Revision != 3 || items[2].Revision != 5 {
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
	items, _, _, err := store.ConfigRevisionsSince(ctx, 0, 10)
	if err != nil || len(items) != 1 || items[0].Digest != "first" {
		t.Fatalf("stale publisher created a duplicate revision: %+v err=%v", items, err)
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
	s.Config.Workers = func() *appconfig.WorkersConfig {
		c := testWorkersConfig()
		c.RuntimePools[0].RuntimeToken = token
		return c
	}()
	s.routes()
	ctx := context.Background()
	if response := runtimeConfigRequest(s, token, "/changes?since=117&wait=1s"); response.Code != 409 || !strings.Contains(response.Body.String(), `"config_revision":0`) {
		t.Fatalf("runtime ahead of an empty control-plane history must resync: %d %s", response.Code, response.Body.String())
	}
	payload, _ := json.Marshal(workerConfigProjection{Routes: []workerConfigRoute{{Host: "w.workers.test", Enabled: true, Version: "rel-1", Pool: "default"}}, Versions: map[string]WorkerVersion{}})
	ops, _ := json.Marshal([]workerConfigOp{{Type: "route_upsert", Pool: "default", Host: "w.workers.test"}})
	if _, _, err := store.AppendConfigRevision(ctx, "d1", payload, ops, 512); err != nil {
		t.Fatal(err)
	}
	if r := runtimeConfigRequest(s, token, "/changes?since=0&wait=1s"); r.Code != 409 || !strings.Contains(r.Body.String(), "RESYNC") {
		t.Fatalf("since=0 must resync: %d %s", r.Code, r.Body.String())
	}
	if response := runtimeConfigRequest(s, token, "/changes?since=117&wait=1s"); response.Code != 409 || !strings.Contains(response.Body.String(), `"config_revision":1`) {
		t.Fatalf("runtime ahead of published history must resync: %d %s", response.Code, response.Body.String())
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
	c.RuntimePools = []appconfig.WorkerRuntimePool{
		{ID: "private-a", Access: []string{"private"}, RuntimeService: "runtime-private", ValidatorService: "validator-private", RuntimeToken: strings.Repeat("p", 24), ManagementToken: strings.Repeat("q", 24), Default: true},
		{ID: "public-b", Access: []string{"public"}, RuntimeService: "runtime-public", ValidatorService: "validator-public", RuntimeToken: strings.Repeat("u", 24), ManagementToken: strings.Repeat("v", 24)},
	}
	s.Config.Workers = c
	s.routes()
	ctx := context.Background()
	projection := workerConfigProjection{
		Routes: []workerConfigRoute{
			{Host: "private.workers.test", Enabled: true, Version: "rel-private", Pool: "private-a"},
			{Host: "public.workers.test", Enabled: true, Version: "rel-public", Pool: "public-b"},
		},
		Versions: map[string]WorkerVersion{
			"rel-private": {ID: "rel-private"},
			"rel-public":  {ID: "rel-public"},
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
	if privateSnapshot.Code != http.StatusOK || !strings.Contains(privateSnapshot.Body.String(), `"lazy_routes":true`) || strings.Contains(privateSnapshot.Body.String(), "workers.test") {
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
