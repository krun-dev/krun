package control

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	appconfig "github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/storage"
	workerapp "github.com/krun-dev/krun/internal/workers"
)

func workerConfigFixture() *State {
	st, _, _ := fixture()
	// Hosts are derived from the domain catalog, not stored.
	ws := Worker{Worker: "svc-a", Organization: "org-a", Domains: []string{"internal"}, Environments: map[string]WorkerEnvironment{
		"production": {Name: "production", Enabled: true, Version: "rel-1"},
		"preview":    {Name: "preview", Enabled: false, Version: "rel-1"},
		"empty":      {Name: "empty", Enabled: true},
	}}
	st.Workers.Workers["svc-a"] = ws
	placed := now()
	st.Workers.Placements[workerPlacementKey("svc-a", "default")] = WorkerPlacement{Worker: "svc-a", Organization: "org-a", NetworkProfile: "default", Pool: "default", Role: "primary", State: "active", Revision: 1, Created: placed, Updated: placed}
	st.Workers.Versions["rel-1"] = WorkerVersion{ID: "rel-1", Worker: "svc-a", Organization: "org-a", Environment: "production", Artifact: "art-1", ArchiveDigest: "archive-1", ModuleDigest: "module-1", Variables: map[string]any{"A": "1"}}
	st.Workers.Versions["rel-unused"] = WorkerVersion{ID: "rel-unused", Worker: "svc-a", Organization: "org-a", Environment: "production", Artifact: "art-1"}
	st.Workers.Workers["svc-deleted"] = Worker{Worker: "svc-deleted", Organization: "org-a", Environments: map[string]WorkerEnvironment{}}
	return st
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
	st := workerConfigFixture()
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
}

func TestWorkerConfigDiffOrdering(t *testing.T) {
	st := workerConfigFixture()
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
	next.Versions["rel-2"] = WorkerVersion{ID: "rel-2", Worker: "svc-a", Organization: "org-a", Environment: "production", Artifact: "art-2"}
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

func appendTestConfigDelta(t *testing.T, store *Store, previous int64, ops ...workerConfigOp) {
	t.Helper()
	revision, won, err := store.AppendConfigDelta(t.Context(), previous, nil, ops)
	if err != nil || !won || revision != previous+1 {
		t.Fatalf("append delta: revision=%d won=%v err=%v", revision, won, err)
	}
}

func TestConfigRevisionStoreAndAcks(t *testing.T) {
	store := postgresTestStore(t)
	ctx := t.Context()
	head := int64(storage.WorkerConfigOpsRetain) + 1
	pool := postgresTestPool(store)
	if _, err := pool.Exec(ctx, "INSERT INTO worker_config_revisions (config_revision,digest) SELECT n,'fixture' FROM generate_series(1,$1::bigint) AS n", head); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "UPDATE krun_meta SET value=$1 WHERE key='worker_config_head'", fmt.Sprint(head)); err != nil {
		t.Fatal(err)
	}
	appendTestConfigDelta(t, store, head, workerConfigOp{Type: "route_delete", Host: "stale.workers.test", Pool: "default"})
	items, floor, latest, err := store.ConfigRevisionsSince(ctx, 1, storage.WorkerConfigOpsRetain)
	if err != nil || latest != head+1 || floor != 3 || len(items) != storage.WorkerConfigOpsRetain || items[0].Revision != 3 || items[len(items)-1].Revision != latest {
		t.Fatalf("revisions=%d floor=%d latest=%d err=%v", len(items), floor, latest, err)
	}
	for _, revision := range []int64{latest, latest - 1} {
		if err := store.RecordAck(ctx, WorkerAck{Pod: "pod-1", UID: "uid-1", Pool: "default", Revision: int(revision), Time: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	acks, err := store.ReadAcks(ctx)
	if err != nil || acks["uid-1"].Revision != int(latest) {
		t.Fatalf("acks: %+v %v", acks, err)
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
	if response := runtimeConfigRequest(s, token, "/changes?since=117&wait=1s"); response.Code != 409 || !strings.Contains(response.Body.String(), `"config_revision":0`) {
		t.Fatalf("runtime ahead of an empty control-plane history must resync: %d %s", response.Code, response.Body.String())
	}
	op := workerConfigOp{Type: "route_delete", Pool: "default", Host: "w.workers.test"}
	appendTestConfigDelta(t, store, 0, op)
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
	appendTestConfigDelta(t, store, 1, op)
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
	appendTestConfigDelta(t, store, 0, workerConfigOp{Type: "route_delete", Pool: "private-a", Host: "old.workers.test"})
	privateRoute := workerConfigRoute{Worker: "svc-a", Host: "private.workers.test", Enabled: true, Pool: "private-a"}
	publicRoute := workerConfigRoute{Worker: "svc-a", Host: "public.workers.test", Enabled: true, Pool: "public-b"}
	appendTestConfigDelta(t, store, 1,
		workerConfigOp{Type: "route_upsert", Pool: privateRoute.Pool, Host: privateRoute.Host, Route: &privateRoute},
		workerConfigOp{Type: "route_upsert", Pool: publicRoute.Pool, Host: publicRoute.Host, Route: &publicRoute},
	)

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
