package control

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

func TestPostgresActiveBuildLimitIsExclusive(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	if _, err := store.pool.Exec(ctx, `INSERT INTO krun_worker_services
		(service_id, org_id, active_build_count) VALUES ('svc-limit','org-limit',99)`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < workerActiveBuildLimit-1; i++ {
		if _, err := store.pool.Exec(ctx, `INSERT INTO krun_worker_builds
			(build_id, org_id, service_id, env_name, state, created_at)
			VALUES ($1,'org-limit','svc-limit','production','queued',now())`, fmt.Sprintf("seed-%03d", i)); err != nil {
			t.Fatal(err)
		}
	}

	ready := make(chan struct{}, 2)
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, id := range []string{"candidate-a", "candidate-b"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			errs <- store.Update(ctx, PartitionWorkers, func(st *State) error {
				st.Workers.Builds[id] = WorkerBuild{ID: id, Organization: "org-limit", Service: "svc-limit", Environment: "production", State: "queued", Created: now()}
				ready <- struct{}{}
				<-start
				return nil
			})
		}(id)
	}
	<-ready
	<-ready
	close(start)
	wg.Wait()
	close(errs)

	succeeded, limited := 0, 0
	for err := range errs {
		if err == nil {
			succeeded++
			continue
		}
		if apiErr, ok := err.(*APIError); ok && apiErr.Code == "BUILD_LIMIT" {
			limited++
			continue
		}
		t.Fatalf("unexpected insertion error: %v", err)
	}
	if succeeded != 1 || limited != 1 {
		t.Fatalf("succeeded=%d limited=%d", succeeded, limited)
	}
	var counter, rows int
	if err := store.pool.QueryRow(ctx, `SELECT active_build_count FROM krun_worker_services WHERE service_id='svc-limit'`).Scan(&counter); err != nil {
		t.Fatal(err)
	}
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM krun_worker_builds
		WHERE service_id='svc-limit' AND state NOT IN ('succeeded','failed')`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if counter != workerActiveBuildLimit || rows != workerActiveBuildLimit {
		t.Fatalf("counter=%d rows=%d", counter, rows)
	}
}

func TestPostgresBuildFinishDecrementsLimitOnce(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	if _, err := store.pool.Exec(ctx, `INSERT INTO krun_worker_services
		(service_id, org_id) VALUES ('svc-finish','org-finish')`); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(ctx, PartitionWorkers, func(st *State) error {
		st.Workers.Builds["build-finish"] = WorkerBuild{ID: "build-finish", Organization: "org-finish", Service: "svc-finish", Environment: "production", State: "queued", Created: now()}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := store.finishBuild(ctx, "build-finish", "failed", "expected", "", "log"); err != nil {
			t.Fatal(err)
		}
	}
	var counter int
	if err := store.pool.QueryRow(ctx, `SELECT active_build_count FROM krun_worker_services WHERE service_id='svc-finish'`).Scan(&counter); err != nil {
		t.Fatal(err)
	}
	if counter != 0 {
		t.Fatalf("repeated terminal transition decremented counter to %d", counter)
	}
}
