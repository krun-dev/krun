package storage

import (
	"testing"

	"github.com/krun-dev/krun/internal/domain"
)

func TestDeletedWorkerCleanupIsDurableAndFencesBuilds(t *testing.T) {
	s := postgresTestStore(t)
	ctx := t.Context()
	if err := s.Update(ctx, PartitionIAM, func(st *domain.State) error {
		st.Organizations["org-cleanup"] = domain.Organization{ID: "org-cleanup", Name: "Org"}
		st.Resources["worker-cleanup"] = domain.Resource{ID: "worker-cleanup", Kind: "worker", Organization: "org-cleanup", Name: "Worker"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Update(ctx, PartitionWorkers, func(st *domain.State) error {
		st.Workers.Workers["worker-cleanup"] = domain.Worker{Worker: "worker-cleanup", Organization: "org-cleanup", Environments: map[string]domain.WorkerEnvironment{"production": {Name: "production", Enabled: true}}}
		st.Workers.Builds["build-cleanup"] = domain.WorkerBuild{ID: "build-cleanup", Worker: "worker-cleanup", Organization: "org-cleanup", State: "queued", Job: "legacy-job", Created: now()}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Update(ctx, PartitionIAM, func(st *domain.State) error { delete(st.Resources, "worker-cleanup"); return nil }); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimBuild(ctx, "build-cleanup", "replica")
	if err != nil || claimed {
		t.Fatalf("deleted Worker build claimed=%v err=%v", claimed, err)
	}
	if err := s.RetainExpired(ctx); err != nil {
		t.Fatal(err)
	}
	jobs, err := s.WorkerCleanupLegacyJobs(ctx, "worker-cleanup", "", 100)
	if err != nil || len(jobs) != 1 || jobs[0] != "legacy-job" {
		t.Fatalf("legacy jobs %v: %v", jobs, err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE krun_worker_cleanup SET next_attempt_at=now() WHERE worker_id='worker-cleanup'`); err != nil {
		t.Fatal(err)
	}
	tasks, err := s.PendingWorkerCleanups(ctx, 10)
	if err != nil || len(tasks) != 1 || tasks[0].Organization != "org-cleanup" {
		t.Fatalf("pending tasks %+v: %v", tasks, err)
	}
	if err := s.UpdateWorkerCleanup(ctx, "worker-cleanup", false, "temporary failure"); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateWorkerCleanup(ctx, "worker-cleanup", true, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE krun_worker_cleanup SET completed_at=now()-interval '32 days' WHERE worker_id='worker-cleanup'`); err != nil {
		t.Fatal(err)
	}
	if err := s.RetainExpired(ctx); err != nil {
		t.Fatal(err)
	}
	jobs, err = s.WorkerCleanupLegacyJobs(ctx, "worker-cleanup", "", 100)
	if err != nil || len(jobs) != 0 {
		t.Fatalf("cleanup retained jobs %v: %v", jobs, err)
	}
}
