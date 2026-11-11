package control

import (
	"context"
	"net/http/httptest"
	"testing"
)

func TestPostgresWorkerSearchBeforePagination(t *testing.T) {
	store := postgresTestStore(t)
	seedPerformanceSites(t, store, 1000)
	ctx := context.Background()
	scope := stateReadScope{
		User: "actor", Organization: "tenant", Kind: "worker", Summary: true,
		Search: "sItE 99", Limit: 3,
	}
	st, err := store.Open(withStateScope(ctx, scope), PartitionAll)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"site-000990", "site-000992", "site-000994"} {
		resource, exists := st.Resources[id]
		if !exists || resource.Created.IsZero() {
			t.Fatalf("search missed resource/time %s: %+v", id, st.Resources)
		}
	}
	if len(st.Resources) != 3 || len(st.Workers.Workers) != 3 || len(st.Workers.Builds) != 0 {
		t.Fatalf("search scope not bounded: %+v", st.Resources)
	}
	scope.After = "site-000992"
	st, err = store.Open(withStateScope(ctx, scope), PartitionAll)
	if err != nil || len(st.Resources) != 3 || st.Resources["site-000994"].ID == "" || st.Resources["site-000998"].ID == "" {
		t.Fatalf("search cursor: %+v %v", st.Resources, err)
	}
	scope.Search = "%"
	scope.After = ""
	st, err = store.Open(withStateScope(ctx, scope), PartitionAll)
	if err != nil || len(st.Resources) != 0 {
		t.Fatalf("search treated wildcard as pattern: %+v %v", st.Resources, err)
	}
	scope.Search = "SITE-000998"
	st, err = store.Open(withStateScope(ctx, scope), PartitionAll)
	if err != nil || len(st.Resources) != 1 || st.Resources["site-000998"].ID == "" {
		t.Fatalf("ID search: %+v %v", st.Resources, err)
	}
	if _, err := postgresTestPool(store).Exec(ctx, `INSERT INTO krun_resources(resource_id,kind,org_id,name) VALUES
		('case-A','worker','tenant','Cursor case'),
		('case-_B','worker','tenant','Cursor case'),
		('case-a','worker','tenant','Cursor case')`); err != nil {
		t.Fatal(err)
	}
	scope.Search = "Cursor case"
	scope.After = "case-Z"
	st, err = store.Open(withStateScope(ctx, scope), PartitionAll)
	if err != nil || len(st.Resources) != 2 || st.Resources["case-_B"].ID == "" || st.Resources["case-a"].ID == "" {
		t.Fatalf("cursor collation differs from Go ordering: %+v %v", st.Resources, err)
	}
	server := &Server{Store: store}
	server.Config.Workers = testWorkersConfig()
	server.routes()
	r := httptest.NewRequest("GET", "/api/v1/orgs/tenant/workers?worker_id=site-000002&q=missing&limit=1&after=zzz", nil)
	r.Pattern = "GET /api/v1/orgs/{org}/workers"
	r.SetPathValue("org", "tenant")
	r, err = server.scopedRequest(r, authCredentials{session: Session{User: "actor"}})
	if err != nil {
		t.Fatal(err)
	}
	st, err = store.Open(r.Context(), PartitionAll)
	if err != nil || len(st.Resources) != 1 || st.Resources["site-000002"].ID == "" {
		t.Fatalf("detail inherited list filter: %+v %v", st.Resources, err)
	}
}
