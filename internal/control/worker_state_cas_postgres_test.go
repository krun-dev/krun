package control

import (
	"context"
	"strings"
	"testing"
)

func TestOperationEdgeRevisionIsFrozenInMemory(t *testing.T) {
	store := memoryStore()
	store.memory.Workers.Operations["op-edge"] = WorkerOperation{ID: "op-edge", State: "committed"}
	first, err := store.setOperationEdgeRevision(context.Background(), "op-edge", 7)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.setOperationEdgeRevision(context.Background(), "op-edge", 29)
	if err != nil {
		t.Fatal(err)
	}
	if first != 7 || second != 7 || store.memory.Workers.Operations["op-edge"].EdgeRevision != 7 {
		t.Fatalf("edge revision moved: first=%d second=%d stored=%d", first, second, store.memory.Workers.Operations["op-edge"].EdgeRevision)
	}
}

func TestPostgresOperationEdgeRevisionIsFrozen(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	if _, err := store.pool.Exec(ctx, `INSERT INTO krun_worker_operations
		(operation_id, kind, org_id, service_id, env_name, state)
		VALUES ('op-edge','publish','org','service','production','committed')`); err != nil {
		t.Fatal(err)
	}
	first, err := store.setOperationEdgeRevision(ctx, "op-edge", 11)
	if err != nil {
		t.Fatal(err)
	}
	if first != 11 {
		t.Fatalf("first edge revision = %d, want 11", first)
	}
	// Model unrelated config churn after this operation committed. Its durable
	// acknowledgement target must stay at the first containing revision.
	second, err := store.setOperationEdgeRevision(ctx, "op-edge", 37)
	if err != nil {
		t.Fatal(err)
	}
	if second != 11 {
		t.Fatalf("edge revision followed global head: got %d, want 11", second)
	}
	var stored int64
	if err = store.pool.QueryRow(ctx, `SELECT edge_revision FROM krun_worker_operations WHERE operation_id='op-edge'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != 11 {
		t.Fatalf("stored edge revision = %d, want 11", stored)
	}
}

func TestPostgresStaleSnapshotCannotReviveFinishedBuild(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	if _, err := store.pool.Exec(ctx, `INSERT INTO krun_worker_services
		(service_id, org_id) VALUES ('svc-cas','org-cas')`); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(ctx, PartitionWorkers, func(st *State) error {
		st.Workers.Builds["build-cas"] = WorkerBuild{
			ID: "build-cas", Organization: "org-cas", Service: "svc-cas",
			Environment: "production", State: "queued", Created: now(),
		}
		st.Workers.BuildPrivate["build-cas"] = WorkerBuildPrivate{Actor: WorkerActor{User: "user", AuthVersion: 1}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if claimed, err := store.claimBuild(ctx, "build-cas", "replica-a"); err != nil || !claimed {
		t.Fatalf("claim: claimed=%t err=%v", claimed, err)
	}

	loaded := make(chan struct{})
	resume := make(chan struct{})
	staleResult := make(chan error, 1)
	go func() {
		staleResult <- store.Update(ctx, PartitionWorkers, func(st *State) error {
			build := st.Workers.Builds["build-cas"]
			close(loaded)
			<-resume
			build.Repository = &WorkerBuildRepository{WorkerRepositorySource: WorkerRepositorySource{ProviderID: "github", Repository: "owner/repo"}}
			st.Workers.Builds[build.ID] = build
			return nil
		})
	}()
	<-loaded
	if err := store.finishBuild(ctx, "build-cas", "failed", "finished elsewhere", "", "log"); err != nil {
		t.Fatal(err)
	}
	close(resume)
	if err := <-staleResult; err == nil || !strings.Contains(err.Error(), "build changed") {
		t.Fatalf("stale snapshot write = %v, want CAS conflict", err)
	}

	var state string
	var active int
	if err := store.pool.QueryRow(ctx, `SELECT state FROM krun_worker_builds WHERE build_id='build-cas'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if err := store.pool.QueryRow(ctx, `SELECT active_build_count FROM krun_worker_services WHERE service_id='svc-cas'`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || active != 0 {
		t.Fatalf("stale snapshot revived build: state=%q active_build_count=%d", state, active)
	}
}

func TestPostgresStaleBuildOwnerCannotFinishAfterTakeover(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	if _, err := store.pool.Exec(ctx, `INSERT INTO krun_worker_services
		(service_id, org_id) VALUES ('svc-owner','org-owner')`); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(ctx, PartitionWorkers, func(st *State) error {
		st.Workers.Builds["build-owner"] = WorkerBuild{
			ID: "build-owner", Organization: "org-owner", Service: "svc-owner",
			Environment: "production", State: "queued", Created: now(),
		}
		st.Workers.BuildPrivate["build-owner"] = WorkerBuildPrivate{Actor: WorkerActor{User: "user", AuthVersion: 1}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if claimed, err := store.claimBuild(ctx, "build-owner", "replica-old"); err != nil || !claimed {
		t.Fatalf("old claim: claimed=%t err=%v", claimed, err)
	}
	if _, err := store.pool.Exec(ctx, `UPDATE krun_worker_builds SET claimed_at=now()-interval '2 minutes'
		WHERE build_id='build-owner'`); err != nil {
		t.Fatal(err)
	}
	if claimed, err := store.claimBuild(ctx, "build-owner", "replica-new"); err != nil || !claimed {
		t.Fatalf("takeover: claimed=%t err=%v", claimed, err)
	}
	if err := store.finishClaimedBuild(ctx, "build-owner", "replica-old", "failed", "stale", "", "stale"); err != nil {
		t.Fatal(err)
	}
	var state, owner string
	var active int
	if err := store.pool.QueryRow(ctx, `SELECT state, claimed_by FROM krun_worker_builds WHERE build_id='build-owner'`).Scan(&state, &owner); err != nil {
		t.Fatal(err)
	}
	if err := store.pool.QueryRow(ctx, `SELECT active_build_count FROM krun_worker_services WHERE service_id='svc-owner'`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if state != "building" || owner != "replica-new" || active != 1 {
		t.Fatalf("stale owner finished build: state=%q owner=%q active=%d", state, owner, active)
	}
	if err := store.finishClaimedBuild(ctx, "build-owner", "replica-new", "failed", "expected", "", "new"); err != nil {
		t.Fatal(err)
	}
	if err := store.pool.QueryRow(ctx, `SELECT state FROM krun_worker_builds WHERE build_id='build-owner'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if err := store.pool.QueryRow(ctx, `SELECT active_build_count FROM krun_worker_services WHERE service_id='svc-owner'`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || active != 0 {
		t.Fatalf("current owner did not finish: state=%q active=%d", state, active)
	}
}
