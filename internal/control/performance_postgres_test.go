package control

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	appconfig "github.com/krun-dev/krun/internal/config"
	workerapp "github.com/krun-dev/krun/internal/workers"
)

func seedPerformanceSites(t *testing.T, s *Store, n int) {
	t.Helper()
	ctx := context.Background()
	statements := []string{
		`INSERT INTO krun_users(user_id,source,subject,name,status) VALUES('actor','local','actor','Actor','active')`,
		`INSERT INTO krun_organizations(org_id,name) VALUES('tenant','Tenant'),('other','Other')`,
		`INSERT INTO krun_members(org_id,user_id) VALUES('tenant','actor')`,
		`INSERT INTO krun_assignments(assignment_id,subject_kind,subject_id,role,scope_kind,scope_id) VALUES('grant','user','actor','org_admin','organization','tenant')`,
		`INSERT INTO krun_resources(resource_id,kind,org_id,name) SELECT 'site-'||repeat('0',greatest(0,6-length(i::text)))||i::text,'service',CASE WHEN i%2=0 THEN 'tenant' ELSE 'other' END,'Site '||i FROM generate_series(1,$1::int)i`,
		`INSERT INTO krun_worker_services(service_id,org_id,network_profile) SELECT resource_id,org_id,'default' FROM krun_resources`,
		`INSERT INTO krun_worker_service_placements(service_id,pool_id,org_id,network_profile,role,state,revision,created_at,updated_at) SELECT resource_id,'default',org_id,'default','primary','active',1,now(),now() FROM krun_resources`,
		`INSERT INTO krun_worker_environments(service_id,env_name,enabled,config_revision,variables) SELECT resource_id,'production',true,1,'{}'::jsonb FROM krun_resources`,
	}
	for _, query := range statements {
		var args []any
		if query == statements[4] {
			args = []any{n}
		}
		if _, err := postgresTestPool(s).Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPostgresScopedSiteReadsAndPagination(t *testing.T) {
	s := postgresTestStore(t)
	seedPerformanceSites(t, s, 1000)
	ctx := context.Background()
	scope := stateReadScope{User: "actor", Organization: "tenant", Service: "site-000002", Kind: "service", Limit: 100}
	st, err := s.Open(withStateScope(ctx, scope), PartitionAll)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Resources) != 1 || len(st.Workers.Services) != 1 || len(st.Organizations) != 1 || len(st.Users) != 1 {
		t.Fatalf("unbounded scoped load: resources=%d services=%d orgs=%d users=%d", len(st.Resources), len(st.Workers.Services), len(st.Organizations), len(st.Users))
	}
	p := Principal{User: st.Users["actor"]}
	d, err := Authorize(st, p, "service.read", Target{Kind: "service", ID: scope.Service, Organization: "tenant", Environment: "production"})
	if err != nil || !d.Allowed {
		t.Fatalf("actor grant lost: %+v %v", d, err)
	}
	scope.Service = "site-000001"
	st, err = s.Open(withStateScope(ctx, scope), PartitionAll)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Resources) != 0 || len(st.Workers.Services) != 0 {
		t.Fatal("cross-tenant entity loaded")
	}
	scope.Service = ""
	scope.Summary = true
	scope.Limit = 37
	seen := map[string]bool{}
	for page := 0; page < 20; page++ {
		st, err = s.Open(withStateScope(ctx, scope), PartitionAll)
		if err != nil {
			t.Fatal(err)
		}
		if len(st.Resources) > 37 || len(st.Workers.Services) > 37 || len(st.Workers.Builds) != 0 {
			t.Fatal("unbounded summary page")
		}
		for id := range st.Resources {
			if seen[id] {
				t.Fatalf("duplicate cursor result %s", id)
			}
			seen[id] = true
		}
		r := httptest.NewRequest("GET", "/api/v1/orgs/tenant/services", nil).WithContext(withStateScope(ctx, scope))
		result, _, _ := resourcePageItems(st, r, nil)
		next, _ := result.(map[string]any)["next_after"].(string)
		if next == "" {
			break
		}
		scope.After = next
	}
	if len(seen) != 500 {
		t.Fatalf("pagination omitted sites: %d", len(seen))
	}
}

func TestPostgresDeltaPublisherOnlyWritesChangedSite(t *testing.T) {
	s := postgresTestStore(t)
	seedPerformanceSites(t, s, 20)
	ctx := context.Background()
	config := workerProjectionConfig([]appconfig.WorkerDomain{{ID: "internal", Domain: "workers.test"}})

	controller := &workerapp.Controller{Service: (&Server{Store: s}).workerApplication(nil), Config: config}
	rev, err := controller.PublishConfig(ctx)
	if err != nil || rev != 1 {
		t.Fatalf("initial delta: %d %v", rev, err)
	}
	var before string
	if err = postgresTestPool(s).QueryRow(ctx, `SELECT xmin::text FROM worker_config_routes WHERE service_id='site-000004'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err = postgresTestPool(s).Exec(ctx, `UPDATE krun_worker_environments SET enabled=false WHERE service_id='site-000002'`); err != nil {
		t.Fatal(err)
	}
	rev, err = controller.PublishConfig(ctx)
	if err != nil || rev != 2 {
		t.Fatalf("single delta: %d %v", rev, err)
	}
	var after string
	var count int
	if err = postgresTestPool(s).QueryRow(ctx, `SELECT xmin::text FROM worker_config_routes WHERE service_id='site-000004'`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("unrelated route was rewritten")
	}
	if err = postgresTestPool(s).QueryRow(ctx, `SELECT count(*) FROM worker_config_ops WHERE config_revision=2`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("single-route delta has %d ops: %v", count, err)
	}
	dirty, err := s.DirtyWorkerServices(ctx)
	if err != nil || len(dirty) != 0 {
		t.Fatalf("queue not acknowledged: %+v %v", dirty, err)
	}
	var route workerConfigRoute
	row := postgresTestPool(s).QueryRow(ctx, `SELECT host FROM worker_config_routes WHERE service_id='site-000002'`)
	if err = row.Scan(&route.Host); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", "/api/internal/workers/config/routes/"+route.Host, nil)
	request.SetPathValue("host", route.Host)
	request.Header.Set("X-Runtime-Pool", "default")
	request.Header.Set("Authorization", "Bearer fixture")
	config.RuntimePools[0].RuntimeToken = "fixture"
	server := &Server{Store: s, Config: appconfig.Config{Workers: config}}
	response := httptest.NewRecorder()
	server.workerRuntimeConfigRoute(response, request)
	if response.Code != 200 {
		t.Fatalf("resolve=%d %s", response.Code, response.Body.String())
	}
	var snap workerapp.WorkerConfigSnapshot
	if err = json.Unmarshal(response.Body.Bytes(), &snap); err != nil || len(snap.Routes) != 1 || snap.Routes[0].Enabled {
		t.Fatalf("resolved stale route: %+v %v", snap, err)
	}
}

func TestPostgresWorkQueueRotatesPastFirstHundred(t *testing.T) {
	s := postgresTestStore(t)
	ctx := context.Background()
	if _, err := postgresTestPool(s).Exec(ctx, `INSERT INTO krun_worker_builds(build_id,org_id,service_id,env_name,state,created_at) SELECT 'b-'||lpad(i::text,4,'0'),'org','site','production','queued',now()-'1 day'::interval FROM generate_series(1,250)i`); err != nil {
		t.Fatal(err)
	}
	first, err := s.PendingWork(ctx, 100)
	if err != nil || len(first.Builds) != 100 {
		t.Fatal(err)
	}
	for id := range first.Builds {
		if err = s.ScheduleWorkerRetry(ctx, "build", id); err != nil {
			t.Fatal(err)
		}
	}
	second, err := s.PendingWork(ctx, 100)
	if err != nil || len(second.Builds) != 100 {
		t.Fatal(err)
	}
	for id := range second.Builds {
		if _, ok := first.Builds[id]; ok {
			t.Fatal("old admission window starved new work")
		}
	}
	for id := range second.Builds {
		ok, err := s.ClaimBuild(ctx, id, "replica")
		if err != nil || !ok {
			t.Fatal(err)
		}
		started, err := s.WorkerExecutionStarted(ctx, "build", id, time.Time{})
		if err != nil || time.Since(started) > time.Second {
			t.Fatalf("queue age consumed execution timeout: %v %v", started, err)
		}
		break
	}
}

func TestPostgresStaleDirtyGenerationCannotPublishOrDropNewerWork(t *testing.T) {
	s := postgresTestStore(t)
	seedPerformanceSites(t, s, 2)
	ctx := context.Background()
	dirty, err := s.DirtyWorkerServices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = postgresTestPool(s).Exec(ctx, `UPDATE krun_worker_environments SET enabled=false WHERE service_id=$1`, dirty[0].ID); err != nil {
		t.Fatal(err)
	}
	_, won, err := s.AppendConfigDelta(ctx, 0, dirty, []workerConfigOp{{Type: "route_delete", Pool: "default", Host: "stale.test"}})
	if err != nil || won {
		t.Fatalf("stale projection won=%v err=%v", won, err)
	}
	remaining, err := s.DirtyWorkerServices(ctx)
	if err != nil || len(remaining) != 2 {
		t.Fatalf("new mutation was lost: %v %v", remaining, err)
	}
	controller := &workerapp.Controller{Service: (&Server{Store: s}).workerApplication(nil), Config: workerProjectionConfig([]appconfig.WorkerDomain{{ID: "internal", Domain: "workers.test"}})}
	revision, err := controller.PublishConfig(ctx)
	if err != nil || revision != 1 {
		t.Fatal(err)
	}
	// A no-op publisher must return the current head, never its stale expected
	// head, or an operation could accept an ack that predates its publication.
	revision, won, err = s.AppendConfigDelta(ctx, 0, nil, nil)
	if err != nil || !won || revision != 1 {
		t.Fatalf("no-op returned stale acknowledgement target: %d %v %v", revision, won, err)
	}
}

func TestPostgresLoginAndStartupDoNotLoadSiteCatalogue(t *testing.T) {
	store := postgresTestStore(t)
	seedPerformanceSites(t, store, 1000)
	ctx := context.Background()
	state, err := store.Open(context.WithValue(ctx, loginReadScopeKey{}, loginReadScope{Source: "local", Subject: "actor"}), PartitionIAM)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Users) != 1 || len(state.Resources) != 0 || len(state.Organizations) != 0 || len(state.Assignments) != 0 {
		t.Fatal("login loaded unrelated IAM state")
	}
	config := appconfig.Config{DB: os.Getenv("KRUN_TEST_POSTGRES_URL"), Development: true, PublicURL: "http://127.0.0.1:4381", Providers: []appconfig.ProviderConfig{{ID: "local", Type: "dev", Source: "local", Name: "Development", Registration: "jit"}}}
	if _, err = NewServer(ctx, config, store); err != nil {
		t.Fatal(err)
	}
	var sites int
	if err = postgresTestPool(store).QueryRow(ctx, `SELECT count(*) FROM krun_resources`).Scan(&sites); err != nil || sites != 1000 {
		t.Fatalf("provider registration changed site metadata: %d %v", sites, err)
	}
}
