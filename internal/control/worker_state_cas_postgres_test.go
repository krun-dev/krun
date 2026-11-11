package control

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/krun-dev/krun/internal/domain"
)

func addOperationTestPlacement(st *State, op WorkerOperation) {
	st.Workers.Placements[workerPlacementKey(op.Worker, "default")] = WorkerPlacement{
		Worker: op.Worker, Organization: op.Organization, Pool: "default",
		NetworkProfile: "default", Role: "primary", State: "active",
		Revision: 1, Created: op.Created, Updated: op.Created,
	}
}

func TestPostgresOperationRejectsExpiredClaimAfterTakeover(t *testing.T) {
	store := postgresTestStore(t)
	ctx := t.Context()
	pool := postgresTestPool(store)
	op := WorkerOperation{
		ID: "op-fenced", Organization: "org-fenced", Worker: "svc-fenced",
		Environment: "production", Version: "rel-next", Kind: "publish", State: "queued",
		Created: now(), Steps: []WorkerStep{{Name: "artifact"}, {Name: "candidate"}, {Name: "commit"}, {Name: "synchronize"}},
	}
	if err := store.Update(ctx, PartitionWorkers, func(st *State) error {
		st.Workers.Workers[op.Worker] = Worker{Worker: op.Worker, Organization: op.Organization,
			Environments: map[string]WorkerEnvironment{"production": {Name: "production", Enabled: true}}}
		st.Workers.Operations[op.ID] = op
		addOperationTestPlacement(st, op)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	first, ok, err := store.ClaimOperation(ctx, op.ID, "replica-a")
	if !ok || err != nil {
		t.Fatalf("first claim: ok=%t err=%v", ok, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE krun_worker_operations SET claimed_at=now()-interval '61 seconds',
		reconcile_after=now() WHERE operation_id=$1`, op.ID); err != nil {
		t.Fatal(err)
	}
	if ok, err := store.RenewOperationClaim(ctx, first); ok || err != nil {
		t.Fatalf("expired claim renewed before takeover: ok=%t err=%v", ok, err)
	}
	second, ok, err := store.ClaimOperation(ctx, op.ID, "replica-b")
	if !ok || err != nil {
		t.Fatalf("takeover: ok=%t err=%v", ok, err)
	}
	if ok, err := store.CommitOperation(ctx, first); ok || err != nil {
		t.Fatalf("previous executor committed after takeover: ok=%t err=%v", ok, err)
	}
	if err := store.NoteClaimedOperation(ctx, first, "stale progress"); err != nil {
		t.Fatal(err)
	}
	if err := store.FailClaimedOperation(ctx, first, "stale failure"); err != nil {
		t.Fatal(err)
	}
	var state, message string
	if err := pool.QueryRow(ctx, `SELECT state,error FROM krun_worker_operations WHERE operation_id=$1`, op.ID).Scan(&state, &message); err != nil {
		t.Fatal(err)
	}
	if state != "validating" || message != "" {
		t.Fatalf("previous executor changed state or progress: %q %q", state, message)
	}
	if ok, err := store.RenewOperationClaim(ctx, second); !ok || err != nil {
		t.Fatalf("current claim could not renew: ok=%t err=%v", ok, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE krun_worker_operations SET reconcile_after=now()
		WHERE operation_id=$1`, op.ID); err != nil {
		t.Fatal(err)
	}
	third, ok, err := store.ClaimOperation(ctx, op.ID, "replica-b")
	if !ok || err != nil {
		t.Fatalf("same replica next attempt: ok=%t err=%v", ok, err)
	}
	if !third.StartedAt.Equal(first.StartedAt) {
		t.Fatal("a retry reset the validation deadline")
	}
	if ok, err := store.CommitOperation(ctx, second); ok || err != nil {
		t.Fatalf("stale attempt on the same replica committed: ok=%t err=%v", ok, err)
	}
	if ok, err := store.CommitOperation(ctx, third); !ok || err != nil {
		t.Fatalf("current attempt did not commit: ok=%t err=%v", ok, err)
	}
	if ok, err := store.CommitOperation(ctx, third); ok || err != nil {
		t.Fatalf("repeated commit changed state: ok=%t err=%v", ok, err)
	}
	if err := store.FailClaimedOperation(ctx, third, "late failure after commit"); err != nil {
		t.Fatal(err)
	}
	const edge = 5
	if _, err := pool.Exec(ctx, `UPDATE krun_worker_operations SET edge_revision=$2,edge_pool='default' WHERE operation_id=$1`, op.ID, edge); err != nil {
		t.Fatal(err)
	}
	if err := store.ConfirmOperation(ctx, op.ID, "default", 4); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT state FROM krun_worker_operations WHERE operation_id=$1`, op.ID).Scan(&state); err != nil || state != "committed" {
		t.Fatalf("wrong edge revision or late failure changed state: state=%q err=%v", state, err)
	}
	for range 2 {
		if err := store.ConfirmOperation(ctx, op.ID, "default", edge); err != nil {
			t.Fatal(err)
		}
	}
	var version string
	var revision, history int
	if err := pool.QueryRow(ctx, `SELECT confirmed_version_id,deployment_revision,
		(SELECT count(*) FROM krun_worker_history WHERE operation_id=$2)
		FROM krun_worker_environments WHERE worker_id=$1 AND env_name='production'`, op.Worker, op.ID).
		Scan(&version, &revision, &history); err != nil {
		t.Fatal(err)
	}
	if version != op.Version || revision != 1 || history != 1 {
		t.Fatalf("unexpected convergence: version=%q revision=%d history=%d", version, revision, history)
	}
}

func TestPostgresOperationConfirmationRequiresCurrentDeployment(t *testing.T) {
	store := postgresTestStore(t)
	ctx := t.Context()
	pool := postgresTestPool(store)
	op := WorkerOperation{ID: "op-ack", Worker: "svc-ack", Organization: "org-ack", Environment: "production",
		Version: "rel-ack", State: "committed", Kind: "publish", DeploymentRevision: 1, EdgeRevision: 5, Created: now(),
		Steps: []WorkerStep{{Name: "artifact"}, {Name: "candidate"}, {Name: "commit"}, {Name: "synchronize"}}}
	if err := store.Update(ctx, PartitionWorkers, func(st *State) error {
		st.Workers.Workers[op.Worker] = Worker{Worker: op.Worker, Organization: op.Organization,
			Environments: map[string]WorkerEnvironment{"production": {Name: "production", Enabled: true, Version: op.Version, Revision: 2}}}
		st.Workers.Operations[op.ID] = op
		addOperationTestPlacement(st, op)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE krun_worker_operations SET edge_pool='default' WHERE operation_id=$1`, op.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.ConfirmOperation(ctx, op.ID, "default", int64(op.EdgeRevision)); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := pool.QueryRow(ctx, `SELECT state FROM krun_worker_operations WHERE operation_id=$1`, op.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "committed" {
		t.Fatalf("stale deployment was confirmed: state=%q", state)
	}
}

func TestPostgresRollbackSupersedesUnconfirmedPublish(t *testing.T) {
	store := postgresTestStore(t)
	ctx := t.Context()
	pool := postgresTestPool(store)
	steps := []WorkerStep{{Name: "artifact"}, {Name: "candidate"}, {Name: "commit"}, {Name: "synchronize"}}
	publish := WorkerOperation{ID: "op-publish", Worker: "svc-rollback", Organization: "org-rollback",
		Environment: "production", Version: "rel-new", State: "queued", Kind: "publish", Steps: steps, Created: now()}
	if err := store.Update(ctx, PartitionWorkers, func(st *State) error {
		st.Workers.Workers[publish.Worker] = Worker{Worker: publish.Worker, Organization: publish.Organization,
			Environments: map[string]WorkerEnvironment{"production": {Name: "production", Enabled: true, Version: "rel-stable", Confirmed: "rel-stable"}}}
		st.Workers.Operations[publish.ID] = publish
		addOperationTestPlacement(st, publish)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	commit := func(id, replica string, edge int64) {
		t.Helper()
		claim, ok, err := store.ClaimOperation(ctx, id, replica)
		if err != nil || !ok {
			t.Fatalf("claim %s: ok=%t err=%v", id, ok, err)
		}
		if ok, err := store.CommitOperation(ctx, claim); err != nil || !ok {
			t.Fatalf("commit %s: ok=%t err=%v", id, ok, err)
		}
		if _, err := pool.Exec(ctx, `UPDATE krun_worker_operations SET edge_revision=$2,edge_pool='default' WHERE operation_id=$1`, id, edge); err != nil {
			t.Fatal(err)
		}
	}
	commit(publish.ID, "replica-a", 11)

	rollback := WorkerOperation{ID: "op-rollback", Worker: publish.Worker, Organization: publish.Organization,
		Environment: publish.Environment, Version: "rel-stable", ExpectedRevision: 1,
		State: "queued", Kind: "rollback", Steps: steps, Created: now()}
	if err := store.Update(ctx, PartitionWorkers, func(st *State) error {
		st.Workers.Operations[rollback.ID] = rollback
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	commit(rollback.ID, "replica-b", 12)

	// A restarted observer can resume the committed operation without a claim.
	if _, err := pool.Exec(ctx, `UPDATE krun_worker_operations SET reconcile_after=now()
		WHERE operation_id=$1`, rollback.ID); err != nil {
		t.Fatal(err)
	}
	if scheduled, err := store.ScheduleCommittedOperation(ctx, rollback.ID); err != nil || !scheduled {
		t.Fatalf("resume committed rollback: scheduled=%t err=%v", scheduled, err)
	}
	if err := store.ConfirmOperation(ctx, publish.ID, "default", 11); err != nil {
		t.Fatal(err)
	}
	if err := store.ConfirmOperation(ctx, rollback.ID, "default", 12); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Open(ctx, PartitionWorkers)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Workers.Operations[publish.ID].State != "superseded" ||
		snapshot.Workers.Operations[rollback.ID].State != "available" {
		t.Fatal("late publish ACK changed the state after rollback")
	}
	env := snapshot.Workers.Workers[publish.Worker].Environments[publish.Environment]
	history := snapshot.Workers.Histories[publish.Worker+"/"+publish.Environment]
	if env.Confirmed != rollback.Version || env.Revision != 2 || len(history) != 1 || history[0].Operation != rollback.ID {
		t.Fatalf("unexpected rollback result: version=%q revision=%d history=%+v", env.Confirmed, env.Revision, history)
	}
}

func TestPostgresEnvironmentRemovalSupersedesCommittedDeployment(t *testing.T) {
	for _, mode := range []string{"disable and re-enable", "delete"} {
		t.Run(mode, func(t *testing.T) {
			store := postgresTestStore(t)
			ctx := t.Context()
			op := WorkerOperation{ID: "op-preview", Worker: "svc-preview", Organization: "org-preview",
				Environment: "preview", Version: "rel-preview", State: "committed", Kind: "publish",
				DeploymentRevision: 1, EdgeRevision: 5, Created: now()}
			if err := store.Update(ctx, PartitionWorkers, func(st *State) error {
				st.Workers.Workers[op.Worker] = Worker{Worker: op.Worker, Organization: op.Organization,
					Environments: map[string]WorkerEnvironment{
						"preview": {Name: "preview", Enabled: true, Version: op.Version, Revision: 1},
					}}
				st.Workers.Operations[op.ID] = op
				addOperationTestPlacement(st, op)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			setEnabled := func(enabled bool) {
				t.Helper()
				if err := store.Update(ctx, PartitionWorkers, func(st *State) error {
					worker := st.Workers.Workers[op.Worker]
					env := worker.Environments[op.Environment]
					env.Enabled = enabled
					env.ConfigRevision++
					worker.Environments[op.Environment] = env
					st.Workers.Workers[op.Worker] = worker
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "delete" {
				if err := store.Update(ctx, PartitionWorkers, func(st *State) error {
					worker := st.Workers.Workers[op.Worker]
					delete(worker.Environments, op.Environment)
					worker.PreviewRevision++
					st.Workers.Workers[op.Worker] = worker
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			} else {
				setEnabled(false)
				setEnabled(true)
			}
			if err := store.ConfirmOperation(ctx, op.ID, "default", 5); err != nil {
				t.Fatal(err)
			}
			state, err := store.Open(ctx, PartitionWorkers)
			if err != nil {
				t.Fatal(err)
			}
			got := state.Workers.Operations[op.ID]
			if got.State != "superseded" || got.Finished == nil || len(state.Workers.Histories[op.Worker+"/"+op.Environment]) != 0 {
				t.Fatalf("retired environment deployment accepted a late ACK: %+v", got)
			}
		})
	}
}

func TestPostgresOperationPinsPublishedEdgeRevision(t *testing.T) {
	store := postgresTestStore(t)
	ctx := t.Context()
	pool := postgresTestPool(store)
	if _, err := pool.Exec(ctx, `
		INSERT INTO krun_workers(worker_id,org_id) VALUES('worker','org');
		INSERT INTO krun_worker_placements(worker_id,pool_id,org_id,network_profile,role,state)
			VALUES('worker','default','org','default','primary','active');
		INSERT INTO krun_worker_environments(worker_id,env_name,enabled,version_id,deployment_revision)
			VALUES('worker','production',true,'rel-edge',1);
		INSERT INTO krun_worker_operations(operation_id,kind,org_id,worker_id,env_name,state,version_id,deployment_revision)
			VALUES('op-edge','publish','org','worker','production','committed','rel-edge',1);
		UPDATE krun_meta SET value='10' WHERE key='worker_config_head';
	`); err != nil {
		t.Fatal(err)
	}
	// Another observer can still be holding 10 when the publisher clears the
	// worker's dirty generation. Its stale head must never become an ACK target.
	if got, err := store.PinOperationEdgeRevision(ctx, "op-edge", "default"); err != nil || got != 0 {
		t.Fatalf("dirty worker pinned an edge revision: revision=%d err=%v", got, err)
	}
	dirty, err := store.DirtyWorkers(ctx)
	if err != nil || len(dirty) != 1 {
		t.Fatalf("dirty workers: %v err=%v", dirty, err)
	}
	version := domain.WorkerVersion{ID: "rel-edge", Worker: "worker", Organization: "org", Environment: "production"}
	route := domain.EdgeConfigRoute{Worker: "worker", Host: "worker.example.test", Version: version.ID, Pool: "default", Enabled: true}
	revision, won, err := store.AppendConfigDelta(ctx, 10, dirty, []domain.EdgeConfigOp{
		{Type: "version_put", Pool: "default", Version: &version},
		{Type: "route_upsert", Pool: "default", Host: route.Host, Route: &route},
	})
	if err != nil || !won || revision != 11 {
		t.Fatalf("publish target: revision=%d won=%t err=%v", revision, won, err)
	}
	if got, err := store.PinOperationEdgeRevision(ctx, "op-edge", "wrong-pool"); err != nil || got != 0 {
		t.Fatalf("unpublished pool pinned an edge revision: revision=%d err=%v", got, err)
	}
	first, err := store.PinOperationEdgeRevision(ctx, "op-edge", "default")
	if err != nil {
		t.Fatal(err)
	}
	if first != 11 {
		t.Fatalf("first edge revision = %d, want 11", first)
	}
	// Model unrelated config churn after this operation committed. Its durable
	// acknowledgement target must stay at the first containing revision.
	if _, err := pool.Exec(ctx, `UPDATE krun_meta SET value='37' WHERE key='worker_config_head'`); err != nil {
		t.Fatal(err)
	}
	second, err := store.PinOperationEdgeRevision(ctx, "op-edge", "default")
	if err != nil {
		t.Fatal(err)
	}
	if second != 11 {
		t.Fatalf("edge revision followed global head: got %d, want 11", second)
	}
	var stored int64
	if err = pool.QueryRow(ctx, `SELECT edge_revision FROM krun_worker_operations WHERE operation_id='op-edge'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != 11 {
		t.Fatalf("stored edge revision = %d, want 11", stored)
	}

	move, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer move.Rollback(ctx)
	if _, err := move.Exec(ctx, `UPDATE krun_worker_placements SET pool_id='pool-b' WHERE worker_id='worker'`); err != nil {
		t.Fatal(err)
	}
	ackCtx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	err = store.ConfirmOperation(ackCtx, "op-edge", "default", 11)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("confirmation did not wait for the in-flight placement move: err=%v", err)
	}
	if err := move.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if got, err := store.PinOperationEdgeRevision(ctx, "op-edge", "default"); err != nil || got != 0 {
		t.Fatalf("old pool still owns the ACK target: revision=%d err=%v", got, err)
	}
	if got, err := store.PinOperationEdgeRevision(ctx, "op-edge", "pool-b"); err != nil || got != 0 {
		t.Fatalf("new pool accepted the old pool's revision: revision=%d err=%v", got, err)
	}
	if err := store.ConfirmOperation(ctx, "op-edge", "default", 11); err != nil {
		t.Fatal(err)
	}
	dirty, err = store.DirtyWorkers(ctx)
	if err != nil || len(dirty) != 1 {
		t.Fatalf("dirty workers after pool move: %v err=%v", dirty, err)
	}
	route.Pool = "pool-b"
	revision, won, err = store.AppendConfigDelta(ctx, 37, dirty, []domain.EdgeConfigOp{
		{Type: "version_put", Pool: "pool-b", Version: &version},
		{Type: "route_upsert", Pool: "pool-b", Host: route.Host, Route: &route},
	})
	if err != nil || !won || revision != 38 {
		t.Fatalf("publish in new pool: revision=%d won=%t err=%v", revision, won, err)
	}
	if got, err := store.PinOperationEdgeRevision(ctx, "op-edge", "pool-b"); err != nil || got != 38 {
		t.Fatalf("new pool did not pin its projection: revision=%d err=%v", got, err)
	}
	if err := store.ConfirmOperation(ctx, "op-edge", "pool-b", 37); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := pool.QueryRow(ctx, `SELECT state FROM krun_worker_operations WHERE operation_id='op-edge'`).Scan(&state); err != nil || state != "committed" {
		t.Fatalf("older ACK confirmed the move: state=%q err=%v", state, err)
	}
	if err := store.ConfirmOperation(ctx, "op-edge", "pool-b", 38); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT state FROM krun_worker_operations WHERE operation_id='op-edge'`).Scan(&state); err != nil || state != "available" {
		t.Fatalf("current ACK did not confirm the move: state=%q err=%v", state, err)
	}
}

func TestPostgresStaleSnapshotCannotReviveFinishedBuild(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	if _, err := postgresTestPool(store).Exec(ctx, `INSERT INTO krun_workers
		(worker_id, org_id) VALUES ('svc-cas','org-cas')`); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(ctx, PartitionWorkers, func(st *State) error {
		st.Workers.Builds["build-cas"] = WorkerBuild{
			ID: "build-cas", Organization: "org-cas", Worker: "svc-cas",
			Environment: "production", State: "queued", Created: now(),
		}
		st.Workers.BuildPrivate["build-cas"] = WorkerBuildPrivate{Actor: WorkerActor{User: "user", AuthVersion: 1}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if claimed, err := store.ClaimBuild(ctx, "build-cas", "replica-a"); err != nil || !claimed {
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
	if err := store.FinishBuild(ctx, "build-cas", "failed", "finished elsewhere", "", "log"); err != nil {
		t.Fatal(err)
	}
	close(resume)
	if err := <-staleResult; err == nil || !strings.Contains(err.Error(), "build changed") {
		t.Fatalf("stale snapshot write = %v, want CAS conflict", err)
	}

	var state string
	var active int
	if err := postgresTestPool(store).QueryRow(ctx, `SELECT state FROM krun_worker_builds WHERE build_id='build-cas'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if err := postgresTestPool(store).QueryRow(ctx, `SELECT active_build_count FROM krun_workers WHERE worker_id='svc-cas'`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || active != 0 {
		t.Fatalf("stale snapshot revived build: state=%q active_build_count=%d", state, active)
	}
}

func TestPostgresStaleBuildOwnerCannotFinishAfterTakeover(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	if _, err := postgresTestPool(store).Exec(ctx, `INSERT INTO krun_workers
		(worker_id, org_id) VALUES ('svc-owner','org-owner')`); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(ctx, PartitionWorkers, func(st *State) error {
		st.Workers.Builds["build-owner"] = WorkerBuild{
			ID: "build-owner", Organization: "org-owner", Worker: "svc-owner",
			Environment: "production", State: "queued", Created: now(),
		}
		st.Workers.BuildPrivate["build-owner"] = WorkerBuildPrivate{Actor: WorkerActor{User: "user", AuthVersion: 1}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if claimed, err := store.ClaimBuild(ctx, "build-owner", "replica-old"); err != nil || !claimed {
		t.Fatalf("old claim: claimed=%t err=%v", claimed, err)
	}
	if _, err := postgresTestPool(store).Exec(ctx, `UPDATE krun_worker_builds SET claimed_at=now()-interval '2 minutes'
		WHERE build_id='build-owner'`); err != nil {
		t.Fatal(err)
	}
	if claimed, err := store.ClaimBuild(ctx, "build-owner", "replica-new"); err != nil || !claimed {
		t.Fatalf("takeover: claimed=%t err=%v", claimed, err)
	}
	if err := store.SetClaimedBuildLogKey(ctx, "build-owner", "replica-old", "stale-log"); err == nil {
		t.Fatal("stale owner published log")
	}
	if err := store.SetClaimedBuildLogKey(ctx, "build-owner", "replica-new", "live-log"); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishClaimedBuild(ctx, "build-owner", "replica-old", "failed", "stale", "", "stale"); err != nil {
		t.Fatal(err)
	}
	var state, owner string
	var active int
	if err := postgresTestPool(store).QueryRow(ctx, `SELECT state, claimed_by FROM krun_worker_builds WHERE build_id='build-owner'`).Scan(&state, &owner); err != nil {
		t.Fatal(err)
	}
	if err := postgresTestPool(store).QueryRow(ctx, `SELECT active_build_count FROM krun_workers WHERE worker_id='svc-owner'`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if state != "building" || owner != "replica-new" || active != 1 {
		t.Fatalf("stale owner finished build: state=%q owner=%q active=%d", state, owner, active)
	}
	if err := store.FinishClaimedBuild(ctx, "build-owner", "replica-new", "failed", "expected", "", "new"); err != nil {
		t.Fatal(err)
	}
	if err := postgresTestPool(store).QueryRow(ctx, `SELECT state FROM krun_worker_builds WHERE build_id='build-owner'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if err := postgresTestPool(store).QueryRow(ctx, `SELECT active_build_count FROM krun_workers WHERE worker_id='svc-owner'`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || active != 0 {
		t.Fatalf("current owner did not finish: state=%q active=%d", state, active)
	}
}
