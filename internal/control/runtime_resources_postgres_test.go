package control

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/krun-dev/krun/internal/adapters/configstream"
	"github.com/krun-dev/krun/internal/adapters/kubernetes"
	appconfig "github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/runtimeconfig"
	"github.com/krun-dev/krun/internal/storage"
	workerapp "github.com/krun-dev/krun/internal/workers"
)

func seedRuntimeResourceSite(t *testing.T) (*Store, *appconfig.WorkersConfig, *workerapp.Controller, string) {
	t.Helper()
	store := postgresTestStore(t)
	seedPerformanceSites(t, store, 2)
	config := workerProjectionConfig([]appconfig.WorkerDomain{{ID: "internal", Types: []appconfig.WorkerDomainType{appconfig.WorkerDomainDefault}, Domain: "workers.test"}})
	config.RuntimeConfig.Protocol = 2
	config.RuntimePools[0].RuntimeToken = "fixture-resource-runtime-token"
	config.RuntimePools[0].ManagementToken = "fixture-resource-management-token"
	if err := config.RuntimeConfig.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		`INSERT INTO krun_worker_versions(version_id,org_id,worker_id,env_name,artifact_id) VALUES('version-a','tenant','site-000002','production','artifact-a'),('version-b','tenant','site-000002','production','artifact-b')`,
		`UPDATE krun_worker_environments SET version_id='version-a',deployment_revision=1 WHERE worker_id='site-000002'`,
	} {
		if _, err := postgresTestPool(store).Exec(context.Background(), sql); err != nil {
			t.Fatal(err)
		}
	}
	controller := &workerapp.Controller{Service: (&Server{Store: store}).workerApplication(nil), Config: config, Replica: "resource-test"}
	if _, err := controller.PublishConfig(context.Background()); err != nil {
		t.Fatal(err)
	}
	var host string
	if err := postgresTestPool(store).QueryRow(context.Background(), `SELECT host FROM worker_runtime_bindings WHERE worker_id='site-000002' AND fixed_version_id='' LIMIT 1`).Scan(&host); err != nil {
		t.Fatal(err)
	}
	return store, config, controller, host
}

func TestPostgresRuntimeResourcesSeparateDeploymentFromBindings(t *testing.T) {
	store, _, controller, host := seedRuntimeResourceSite(t)
	ctx := context.Background()
	refs := []domain.ResourceRef{{Kind: "route", ID: "default/" + host}}
	epoch, resources, err := store.RuntimeResources(ctx, "default", refs)
	if err != nil {
		t.Fatal(err)
	}
	before := map[string]domain.ResourceRef{}
	for _, r := range resources {
		before[r.Key()] = r.ResourceRef
	}
	projection, err := runtimeconfig.ResourceSnapshot(resources)
	if err != nil || len(projection.Routes) != 1 || projection.Routes[0].Version != "version-a" {
		t.Fatalf("projection=%+v err=%v", projection, err)
	}
	var oldBindings int
	if err := postgresTestPool(store).QueryRow(ctx, `SELECT count(*) FROM worker_runtime_route_dirty`).Scan(&oldBindings); err != nil {
		t.Fatal(err)
	}
	if _, err = postgresTestPool(store).Exec(ctx, `UPDATE krun_worker_environments SET version_id='version-b',deployment_revision=2 WHERE worker_id='site-000002'`); err != nil {
		t.Fatal(err)
	}
	var dirty int
	if err = postgresTestPool(store).QueryRow(ctx, `SELECT count(*) FROM worker_runtime_route_dirty`).Scan(&dirty); err != nil || dirty != oldBindings {
		t.Fatalf("version switch enqueued route rewrites: %d -> %d: %v", oldBindings, dirty, err)
	}
	_, after, err := store.RuntimeResources(ctx, "default", refs)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range after {
		if r.Kind == "route" && r.Generation != before[r.Key()].Generation {
			t.Fatal("deployment update rewrote route generation")
		}
		if r.Kind == "deployment" && r.Generation <= before[r.Key()].Generation {
			t.Fatal("deployment generation did not advance")
		}
	}
	projection, err = runtimeconfig.ResourceSnapshot(after)
	if err != nil || projection.Routes[0].Version != "version-b" {
		t.Fatalf("current state not read: %+v %v", projection, err)
	}
	var head string
	if err = postgresTestPool(store).QueryRow(ctx, `SELECT value FROM krun_meta WHERE key='worker_config_head'`).Scan(&head); err != nil || head != "0" {
		t.Fatalf("v2 touched global head: %q %v", head, err)
	}
	if _, err = controller.PublishConfig(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.RuntimeResources(ctx, "other", refs); err == nil {
		t.Fatal("cross-pool route accepted")
	}
	if epoch == "" {
		t.Fatal("epoch missing")
	}
}

func TestPostgresRuntimeResourcesEnforcesRouteHostLength(t *testing.T) {
	store := postgresTestStore(t)
	ctx := t.Context()
	host := strings.Repeat(strings.Repeat("a", 63)+".", 3) + strings.Repeat("a", 61)
	ref := domain.ResourceRef{Kind: "route", ID: "default/" + host}
	_, resources, err := store.RuntimeResources(ctx, "default", []domain.ResourceRef{ref})
	if err != nil || len(resources) != 1 || !resources[0].Deleted || resources[0].ID != ref.ID {
		t.Fatalf("253-byte host: %+v, %v", resources, err)
	}
	ref.ID += "a"
	if _, _, err = store.RuntimeResources(ctx, "default", []domain.ResourceRef{ref}); err == nil {
		t.Fatal("accepted a 254-byte route host")
	}
}

func TestPostgresResourceCacheReplicasRepairMissedNotification(t *testing.T) {
	store, config, _, host := seedRuntimeResourceSite(t)
	ctx := context.Background()
	a := runtimeconfig.NewResourceHub(store, config.RuntimeConfig)
	b := runtimeconfig.NewResourceHub(store, config.RuntimeConfig)
	refs := []domain.ResourceRef{{Kind: "route", ID: "default/" + host}}
	for _, hub := range []*runtimeconfig.ResourceHub{a, b} {
		if _, _, err := hub.Resources(ctx, "default", refs); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := postgresTestPool(store).Exec(ctx, `UPDATE krun_worker_environments SET version_id='version-b' WHERE worker_id='site-000002'`); err != nil {
		t.Fatal(err)
	}
	a.Invalidate(domain.ResourceRef{Kind: "deployment", ID: "site-000002/production"})
	_, resources, err := a.Resources(ctx, "default", refs)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := runtimeconfig.ResourceSnapshot(resources)
	if err != nil || projection.Routes[0].Version != "version-b" {
		t.Fatalf("A stale: %+v %v", projection, err)
	}
	// B intentionally receives no notification. The minimum generation read
	// must bypass its cached graph, independently of which replica committed.
	for _, r := range resources {
		if r.Kind == "deployment" {
			_, current, err := b.Resources(ctx, "default", []domain.ResourceRef{r.ResourceRef})
			if err != nil {
				t.Fatal(err)
			}
			var d domain.RuntimeDeployment
			if err = json.Unmarshal(current[0].Payload, &d); err != nil || d.Version != "version-b" {
				t.Fatalf("minimum version read=%+v %v", d, err)
			}
		}
	}
}

func TestPostgresResourceWebSocketColdResolveAndUpdate(t *testing.T) {
	store, config, controller, host := seedRuntimeResourceSite(t)
	configureResourceSessionIdentity(t, controller, config)
	s := &Server{Store: store, Config: appconfig.Config{Workers: config}, workers: controller}
	server := httptest.NewServer(http.HandlerFunc(s.workerRuntimeResourceWatch))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	headers := http.Header{"X-Runtime-Pool": []string{"default"}, "Authorization": []string{"Bearer " + config.RuntimePools[0].RuntimeToken}}
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), &websocket.DialOptions{HTTPHeader: headers})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	write := func(f configstream.Frame) {
		t.Helper()
		if err := conn.Write(ctx, websocket.MessageBinary, configstream.Marshal(f)); err != nil {
			t.Fatal(err)
		}
	}
	read := func() configstream.Frame {
		t.Helper()
		_, raw, err := conn.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		f, err := configstream.Unmarshal(raw)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	write(configstream.Frame{Type: "hello", PodUID: "pod-resource", SessionProof: strings.Repeat("a", 64)})
	welcome := read()
	if welcome.Type != "welcome" {
		t.Fatalf("welcome=%+v", welcome)
	}
	write(configstream.Frame{Type: "resolve", Epoch: welcome.Epoch, RequestID: 1, Refs: []domain.ResourceRef{{Kind: "route", ID: "default/" + host}}})
	initial := read()
	if initial.Type != "snapshot" || initial.RequestID != 1 {
		t.Fatalf("initial=%+v", initial)
	}
	write(configstream.Frame{Type: "ack", Epoch: welcome.Epoch, Refs: initial.Refs})
	write(configstream.Frame{Type: "ready", Epoch: welcome.Epoch})
	deadline := time.Now().Add(3 * time.Second)
	for {
		status, err := store.RuntimePropagation(ctx, "default", "site-000002", "production", []string{"pod-resource"}, true)
		if err != nil {
			t.Fatal(err)
		}
		if status.State == "current" && status.Applied == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("wire ACK did not confirm current membership: %+v", status)
		}
		time.Sleep(20 * time.Millisecond)
	}
	var ledgerRows int
	if err := postgresTestPool(store).QueryRow(ctx, `SELECT count(*) FROM worker_runtime_resource_acks WHERE pod_uid='pod-resource'`).Scan(&ledgerRows); err != nil || ledgerRows != 1 {
		t.Fatalf("immutable resources accumulated ACK rows: %d %v", ledgerRows, err)
	}
	projection, err := runtimeconfig.ResourceSnapshot(initial.Resources)
	if err != nil || projection.Routes[0].Version != "version-a" {
		t.Fatalf("initial projection=%+v %v", projection, err)
	}
	if _, err := postgresTestPool(store).Exec(ctx, `UPDATE krun_worker_environments SET version_id='version-b' WHERE worker_id='site-000002'`); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.PublishConfig(ctx); err != nil {
		t.Fatal(err)
	}
	for {
		update := read()
		if update.Type == "snapshot" {
			for _, resource := range update.Resources {
				if resource.Kind == "deployment" {
					var d domain.RuntimeDeployment
					_ = json.Unmarshal(resource.Payload, &d)
					if d.Version == "version-b" {
						return
					}
				}
			}
		}
	}
}

func TestPostgresRuntimePropagationTracksMembershipAndSessionReplacement(t *testing.T) {
	store, _, _, _ := seedRuntimeResourceSite(t)
	ctx := context.Background()
	refs := []domain.ResourceRef{{Kind: "deployment", ID: "site-000002/production"}}
	epoch, versions, err := store.RuntimeGenerations(ctx, refs)
	if err != nil {
		t.Fatal(err)
	}
	ref := versions[refs[0].Key()]
	for _, pod := range []string{"a", "b"} {
		if err = store.OpenRuntimeSession(ctx, "default", pod, epoch, "session-"+pod); err != nil {
			t.Fatal(err)
		}
		if err = store.TouchRuntimeSession(ctx, pod, "session-"+pod, true, true); err != nil {
			t.Fatal(err)
		}
	}
	if err = store.RecordRuntimeResourceAcks(ctx, "default", "a", epoch, "session-a", []domain.ResourceRef{ref}); err != nil {
		t.Fatal(err)
	}
	check := func(pods []string, confirmed bool) domain.RuntimePropagation {
		t.Helper()
		out, err := store.RuntimePropagation(ctx, "default", "site-000002", "production", pods, confirmed)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	out := check([]string{"a", "b"}, true)
	if out.State != "current" || out.Applied != 1 || out.Unsubscribed != 1 {
		t.Fatalf("working-set propagation=%+v", out)
	}
	out = check([]string{"a", "b", "missing"}, true)
	if out.State != "unknown" || out.Unknown != 1 || out.Total != 3 {
		t.Fatalf("missing Ready pod was excluded: %+v", out)
	}
	if out = check(nil, false); out.State != "unknown" || out.MembershipConfirmed {
		t.Fatalf("unconfirmed membership=%+v", out)
	}
	if err = store.OpenRuntimeSession(ctx, "default", "a", epoch, "replacement"); err != nil {
		t.Fatal(err)
	}
	if err = store.TouchRuntimeSession(ctx, "a", "session-a", true, true); err == nil {
		t.Fatal("replaced connection heartbeat accepted")
	}
	if err = store.CloseRuntimeSession(ctx, "a", "session-a", true); err != nil {
		t.Fatal(err)
	}
	if err = store.TouchRuntimeSession(ctx, "a", "replacement", true, true); err != nil {
		t.Fatal(err)
	}
	out = check([]string{"a"}, true)
	if out.State != "current" || out.Unsubscribed != 1 || out.Rejected != 0 {
		t.Fatalf("old session polluted replacement: %+v", out)
	}
	if err = store.RecordRuntimeResourceAcks(ctx, "default", "a", epoch, "replacement", []domain.ResourceRef{ref}); err != nil {
		t.Fatal(err)
	}
	if _, err = postgresTestPool(store).Exec(ctx, `UPDATE krun_worker_environments SET version_id='version-b' WHERE worker_id='site-000002'`); err != nil {
		t.Fatal(err)
	}
	out = check([]string{"a"}, true)
	if out.State != "pending" || out.Lagging != 1 {
		t.Fatalf("late cache was reported current: %+v", out)
	}
	if err = store.PruneRuntimeResourceAcks(ctx, "a", "replacement", nil); err != nil {
		t.Fatal(err)
	}
	out = check([]string{"a"}, true)
	if out.Unsubscribed != 1 {
		t.Fatalf("unsubscription retained ACK: %+v", out)
	}
	if err = store.CloseRuntimeSession(ctx, "a", "replacement", false); err != nil {
		t.Fatal(err)
	}
	out = check([]string{"a"}, true)
	if out.Unknown != 1 {
		t.Fatalf("disconnected pod reported convergence: %+v", out)
	}
}

func TestPostgresRuntimeResourceRecreationAndVersionDeletion(t *testing.T) {
	store, _, _, _ := seedRuntimeResourceSite(t)
	ctx := context.Background()
	refs := []domain.ResourceRef{{Kind: "organization", ID: "other"}, {Kind: "version", ID: "version-b"}}
	epoch, before, err := store.RuntimeGenerations(ctx, refs)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = postgresTestPool(store).Exec(ctx, `DELETE FROM krun_organizations WHERE org_id='other'; DELETE FROM krun_worker_versions WHERE version_id='version-b'`); err != nil {
		t.Fatal(err)
	}
	_, deleted, err := store.RuntimeGenerations(ctx, refs)
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range refs {
		if deleted[ref.Key()].Generation <= before[ref.Key()].Generation {
			t.Fatal("deletion did not invalidate resource", ref)
		}
	}
	if _, err = postgresTestPool(store).Exec(ctx, `INSERT INTO krun_organizations(org_id,name) VALUES('other','Recreated'); INSERT INTO krun_worker_versions(version_id,org_id,worker_id,env_name,artifact_id) VALUES('version-b','tenant','site-000002','production','artifact-b')`); err != nil {
		t.Fatal(err)
	}
	afterEpoch, recreated, err := store.RuntimeGenerations(ctx, refs)
	if err != nil {
		t.Fatal(err)
	}
	if epoch != afterEpoch {
		t.Fatal("resource recreation rotated global epoch")
	}
	for _, ref := range refs {
		if recreated[ref.Key()].Incarnation == deleted[ref.Key()].Incarnation {
			t.Fatal("resource recreation reused incarnation", ref)
		}
	}
}

func TestPostgresVersionURLWaitsForServingPreparationWithoutSwitchingDeployment(t *testing.T) {
	store, _, controller, _ := seedRuntimeResourceSite(t)
	ctx := context.Background()
	if _, err := postgresTestPool(store).Exec(ctx, `INSERT INTO krun_worker_operations(operation_id,kind,org_id,worker_id,env_name,version_id,state,steps) VALUES('upload-url','version-url','tenant','site-000002','production','version-b','queued','[{"name":"artifact"},{"name":"candidate"},{"name":"commit"},{"name":"synchronize"}]')`); err != nil {
		t.Fatal(err)
	}
	claim, owned, err := store.ClaimOperation(ctx, "upload-url", "url-test")
	if err != nil || !owned {
		t.Fatalf("claim: %v %v", owned, err)
	}
	if won, err := store.CommitVersionURL(ctx, claim, true); err != nil || !won {
		t.Fatalf("commit: %v %v", won, err)
	}
	var state string
	if err = postgresTestPool(store).QueryRow(ctx, `SELECT state FROM krun_worker_operations WHERE operation_id='upload-url'`).Scan(&state); err != nil || state != "committed" {
		t.Fatalf("URL was available before preparation: %s %v", state, err)
	}
	if _, err = controller.PublishConfig(ctx); err != nil {
		t.Fatal(err)
	}
	target, err := store.PinOperationResourceGeneration(ctx, "upload-url", "default")
	if err != nil || target.Generation == 0 {
		t.Fatalf("URL not pinned: %+v %v", target, err)
	}
	if err = store.ConfirmResourceVersionURL(ctx, "upload-url", "default", domain.EdgeConfigCursor{Epoch: target.Epoch, Revision: target.Generation}); err != nil {
		t.Fatal(err)
	}
	var selected string
	if err = postgresTestPool(store).QueryRow(ctx, `SELECT o.state,e.version_id FROM krun_worker_operations o JOIN krun_worker_environments e ON e.worker_id=o.worker_id AND e.env_name=o.env_name WHERE o.operation_id='upload-url'`).Scan(&state, &selected); err != nil || state != "available" || selected != "version-a" {
		t.Fatalf("URL preparation changed traffic: %s %s %v", state, selected, err)
	}
}

func TestPostgresResourceDeploymentRequiresAllPositiveWeightServingProbes(t *testing.T) {
	store, config, controller, _ := seedRuntimeResourceSite(t)
	ctx := context.Background()
	if _, err := postgresTestPool(store).Exec(ctx, `UPDATE krun_worker_environments SET candidate_version_id='version-b',candidate_weight=50000 WHERE worker_id='site-000002';
 INSERT INTO krun_worker_operations(operation_id,kind,org_id,worker_id,env_name,version_id,candidate_version_id,candidate_weight,deployment_revision,state,steps)
 VALUES('probe-release','publish','tenant','site-000002','production','version-a','version-b',50000,1,'committed','[{"name":"artifact","state":"done"},{"name":"candidate","state":"done"},{"name":"commit","state":"done"},{"name":"synchronize","state":"running"}]')`); err != nil {
		t.Fatal(err)
	}
	epoch, err := store.RuntimeEpoch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rejectCandidate := true
	management := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/config-probe" || r.Header.Get("Authorization") != "Bearer "+config.RuntimePools[0].ManagementToken {
			t.Errorf("unexpected management request: %s", r.URL.Path)
			w.WriteHeader(403)
			return
		}
		input := map[string]any{}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
		}
		if input["version_id"] == "version-b" && rejectCandidate {
			w.WriteHeader(409)
			_, _ = w.Write([]byte(`{}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"pod_uid": "pod-serving", "pool": "default", "config_epoch": epoch, "config_protocol": 2, "version_id": input["version_id"], "target_generation": input["generation"]})
	}))
	defer management.Close()
	kube := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "endpointslices") {
			_, _ = w.Write([]byte(`{"items":[{"endpoints":[{"conditions":{"ready":true},"targetRef":{"uid":"pod-serving"}}]}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"items":[{"metadata":{"name":"serving","uid":"pod-serving"},"status":{"podIP":"127.0.0.1","conditions":[{"type":"Ready","status":"True"}]}}]}`))
	}))
	defer kube.Close()
	controller.Kube = &kubernetes.Client{Base: kube.URL, Client: kube.Client(), Namespace: "test"}
	controller.Http = &http.Client{Transport: validationTransport{target: management.URL, base: http.DefaultTransport}}
	op := domain.WorkerOperation{ID: "probe-release", Kind: "publish", Organization: "tenant", Worker: "site-000002", Environment: "production", Version: "version-a", State: "committed", WorkerGradualRelease: domain.WorkerGradualRelease{Candidate: "version-b", CandidateWeight: 50000}}
	state, err := store.EdgeConfigState(ctx, []string{op.Worker})
	if err != nil {
		t.Fatal(err)
	}
	if err = controller.ProcessOperation(ctx, op, *state.Workers, true); err != nil {
		t.Fatal(err)
	}
	var status string
	if err = postgresTestPool(store).QueryRow(ctx, `SELECT state FROM krun_worker_operations WHERE operation_id='probe-release'`).Scan(&status); err != nil || status != "committed" {
		t.Fatalf("candidate preparation bypassed: %s %v", status, err)
	}
	rejectCandidate = false
	if _, err = postgresTestPool(store).Exec(ctx, `UPDATE krun_worker_operations SET reconcile_after=now(),edge_revision=999,edge_pool='default' WHERE operation_id='probe-release'`); err != nil {
		t.Fatal(err)
	}
	if err = controller.ProcessOperation(ctx, op, *state.Workers, true); err != nil {
		t.Fatal(err)
	}
	if err = postgresTestPool(store).QueryRow(ctx, `SELECT state FROM krun_worker_operations WHERE operation_id='probe-release'`).Scan(&status); err != nil || status != "available" {
		t.Fatalf("serving probes did not confirm without legacy ACK: %s %v", status, err)
	}
	var legacy, resource int64
	if err = postgresTestPool(store).QueryRow(ctx, `SELECT edge_revision,resource_generation FROM krun_worker_operations WHERE operation_id='probe-release'`).Scan(&legacy, &resource); err != nil || legacy != 999 || resource < 1 {
		t.Fatalf("v2 target overwrote legacy cursor: legacy=%d resource=%d err=%v", legacy, resource, err)
	}

}

func TestPostgresResourcePublicationPriorityDoesNotDrainUnrelatedBindings(t *testing.T) {
	store, _, _, _ := seedRuntimeResourceSite(t)
	ctx := context.Background()
	if _, err := postgresTestPool(store).Exec(ctx, `INSERT INTO worker_runtime_route_dirty(worker_id) VALUES('site-000001'),('site-000002') ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	rows, err := store.RuntimeRouteDirty(context.WithValue(ctx, storage.WorkerConfigPriorityKey{}, "site-000002"), 128)
	if err != nil || len(rows) != 1 || rows[0].ID != "site-000002" {
		t.Fatalf("deployment drained unrelated bindings: %+v %v", rows, err)
	}
}

// The API identity is deliberately not Ready: a new runtime needs its stream to bootstrap.
func configureResourceSessionIdentity(t *testing.T, controller *workerapp.Controller, config *appconfig.WorkersConfig) {
	t.Helper()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("labelSelector") != "role=krun-runtime,pool=default" {
			t.Errorf("unexpected Pod selector: %s", r.URL)
		}
		io.WriteString(w, `{"items":[{"metadata":{"name":"runtime-0","uid":"pod-resource"},"status":{"podIP":"127.0.0.2"}}]}`)
	}))
	t.Cleanup(api.Close)
	controller.Kube = &kubernetes.Client{Base: api.URL, Namespace: "test", Client: api.Client()}
	controller.Http = &http.Client{Transport: invokeRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "http://127.0.0.2:8083/config-session" || r.Header.Get("Authorization") != "Bearer "+config.RuntimePools[0].ManagementToken {
			t.Errorf("unexpected identity probe: %s", r.URL)
		}
		status := http.StatusOK
		if r.Header.Get("X-Krun-Session-Proof") != strings.Repeat("a", 64) {
			status = http.StatusForbidden
		}
		body, _ := json.Marshal(map[string]any{"pod_uid": "pod-resource", "pool": "default"})
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
	})}
}

func TestPostgresResourceWebSocketRejectsSessionImpersonation(t *testing.T) {
	store, config, controller, _ := seedRuntimeResourceSite(t)
	configureResourceSessionIdentity(t, controller, config)
	s := &Server{Store: store, Config: appconfig.Config{Workers: config}, workers: controller}
	server := httptest.NewServer(http.HandlerFunc(s.workerRuntimeResourceWatch))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	epoch, err := store.RuntimeEpoch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.OpenRuntimeSession(ctx, "default", "pod-resource", epoch, "victim-session"); err != nil {
		t.Fatal(err)
	}
	for _, hello := range []configstream.Frame{
		{Type: "hello", PodUID: "pod-resource", SessionProof: strings.Repeat("b", 64)},
		{Type: "hello", PodUID: "pod-resource"},
		{Type: "hello", PodUID: "unlisted-pod", SessionProof: strings.Repeat("a", 64)},
		{Type: "hello", PodUID: strings.Repeat("p", 129), SessionProof: strings.Repeat("a", 64)},
	} {
		conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), &websocket.DialOptions{
			HTTPHeader: http.Header{"X-Runtime-Pool": {"default"}, "Authorization": {"Bearer " + config.RuntimePools[0].RuntimeToken}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err = conn.Write(ctx, websocket.MessageBinary, configstream.Marshal(hello)); err != nil {
			t.Fatal(err)
		}
		if _, _, err = conn.Read(ctx); err == nil {
			t.Fatal("impersonated stream received welcome")
		}
		conn.CloseNow()
		if err = store.TouchRuntimeSession(ctx, "pod-resource", "victim-session", true, true); err != nil {
			t.Fatalf("unverified connection replaced the victim: %v", err)
		}
	}
	var sessions int
	if err = postgresTestPool(store).QueryRow(ctx, `SELECT count(*) FROM worker_runtime_sessions`).Scan(&sessions); err != nil || sessions != 1 {
		t.Fatalf("unverified connections created sessions: %d %v", sessions, err)
	}
	for _, pod := range []string{"", strings.Repeat("p", 129)} {
		if err = store.OpenRuntimeSession(ctx, "default", pod, epoch, "invalid"); err == nil {
			t.Fatal("storage accepted an invalid Pod UID")
		}
	}
}
