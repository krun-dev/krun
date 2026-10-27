package control

import (
	"context"
	"sync"
	"testing"
	"time"
)

// The rest of the suite runs on the in-memory store, which cannot exercise
// conditional updates or throttled writes. These cases need a real database.

// TestPostgresBuildClaimIsExclusive covers the replacing of the advisory-lock
// leader election: two replicas racing for the same build must not both win.
func TestPostgresBuildClaimIsExclusive(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	if err := store.Update(ctx, PartitionWorkers, func(st *State) error {
		st.Workers.Builds["bld-1"] = WorkerBuild{ID: "bld-1", Organization: "org-1", Service: "svc-1", Environment: "production", State: "queued", Created: now()}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var winners sync.Map
	var wg sync.WaitGroup
	for _, replica := range []string{"replica-a", "replica-b", "replica-c"} {
		wg.Add(1)
		go func(replica string) {
			defer wg.Done()
			claimed, err := store.claimBuild(ctx, "bld-1", replica)
			if err != nil {
				t.Error(err)
				return
			}
			if claimed {
				winners.Store(replica, true)
			}
		}(replica)
	}
	wg.Wait()
	count := 0
	winners.Range(func(any, any) bool { count++; return true })
	if count != 1 {
		t.Fatalf("expected exactly one claimer, got %d", count)
	}
}

// TestPostgresOperationClaimIsExclusive is the release-pipeline equivalent.
func TestPostgresOperationClaimIsExclusive(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	if err := store.Update(ctx, PartitionWorkers, func(st *State) error {
		st.Workers.Operations["op-1"] = WorkerOperation{ID: "op-1", Kind: "publish", Organization: "org-1", Service: "svc-1", Environment: "production", State: "queued", Created: now()}
		st.Workers.OperationPrivate["op-1"] = WorkerOperationPrivate{Actor: WorkerActor{User: "user-1", AuthVersion: 1}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var winners sync.Map
	var wg sync.WaitGroup
	for _, replica := range []string{"replica-a", "replica-b"} {
		wg.Add(1)
		go func(replica string) {
			defer wg.Done()
			claimed, err := store.claimOperation(ctx, "op-1", replica)
			if err != nil {
				t.Error(err)
				return
			}
			if claimed {
				winners.Store(replica, true)
			}
		}(replica)
	}
	wg.Wait()
	count := 0
	winners.Range(func(any, any) bool { count++; return true })
	if count != 1 {
		t.Fatalf("expected exactly one claimer, got %d", count)
	}
	// A stale claim on a validating row can be taken over by another replica.
	claimed, err := store.claimStaleOperation(ctx, "op-1", "replica-c")
	if err != nil {
		t.Fatal(err)
	}
	if claimed {
		t.Error("a fresh claim must not be stolen")
	}
	if _, err := store.pool.Exec(ctx, `UPDATE krun_worker_operations SET claimed_at = now() - interval '5 minutes' WHERE operation_id='op-1'`); err != nil {
		t.Fatal(err)
	}
	claimed, err = store.claimStaleOperation(ctx, "op-1", "replica-c")
	if err != nil {
		t.Fatal(err)
	}
	if !claimed {
		t.Error("an expired lease must be takeover-able")
	}
}

// TestPostgresCommitRequiresMatchingRevision proves a configuration edit that
// lands during validation cannot be overwritten by the older operation.
func TestPostgresCommitRequiresMatchingRevision(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	if err := store.Update(ctx, PartitionWorkers, func(st *State) error {
		st.Workers.Services["svc-1"] = WorkerService{
			Service: "svc-1", Organization: "org-1", Domains: []string{"internal"},
			Environments: map[string]WorkerEnvironment{"production": {Name: "production", Enabled: true, Revision: 3, ConfigRevision: 1}},
		}
		st.Workers.Operations["op-1"] = WorkerOperation{ID: "op-1", Kind: "publish", Organization: "org-1", Service: "svc-1", Environment: "production", State: "validating", Release: "rel-1", ExpectedRevision: 2, Created: now()}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	committed, err := store.commitOperation(ctx, WorkerOperation{ID: "op-1", Service: "svc-1", Environment: "production", Release: "rel-1", ExpectedRevision: 2})
	if err != nil {
		t.Fatal(err)
	}
	if committed {
		t.Fatal("a stale expected_revision must not commit")
	}
	st, err := store.Open(ctx, PartitionWorkers)
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Workers.Operations["op-1"].State; got != "failed" {
		t.Errorf("operation state = %q, want failed", got)
	}
	if got := st.Workers.Services["svc-1"].Environments["production"].Release; got != "" {
		t.Errorf("release = %q, want it untouched", got)
	}
}

// TestPostgresSessionTouchIsThrottled proves an authenticated read does not
// become a write on every request.
func TestPostgresSessionTouchIsThrottled(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	at := now()
	if err := store.insertSession(ctx, "token-1", Session{User: "user-1", CSRF: "csrf", AuthVersion: 1, Created: at, Seen: at, Absolute: at.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	var before time.Time
	if err := store.pool.QueryRow(ctx, `SELECT seen_at FROM krun_sessions`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	// Inside the throttle window nothing changes.
	if err := store.touchSession(ctx, digest("token-1")); err != nil {
		t.Fatal(err)
	}
	var after time.Time
	if err := store.pool.QueryRow(ctx, `SELECT seen_at FROM krun_sessions`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if !after.Equal(before) {
		t.Fatal("touch inside the throttle window must not write")
	}
	// An aged row is refreshed.
	if _, err := store.pool.Exec(ctx, `UPDATE krun_sessions SET seen_at = now() - interval '5 minutes'`); err != nil {
		t.Fatal(err)
	}
	if err := store.touchSession(ctx, digest("token-1")); err != nil {
		t.Fatal(err)
	}
	var refreshed time.Time
	if err := store.pool.QueryRow(ctx, `SELECT seen_at FROM krun_sessions`).Scan(&refreshed); err != nil {
		t.Fatal(err)
	}
	if !refreshed.After(before) {
		t.Fatal("touch outside the throttle window must refresh seen_at")
	}
}

// TestPostgresDeleteCascadesExplicitly covers the no-foreign-key rule: deleting
// a parent must delete its dependents in the same operation.
func TestPostgresDeleteCascadesExplicitly(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	if err := store.Update(ctx, PartitionIAM, func(st *State) error {
		st.Organizations["org-1"] = Organization{ID: "org-1", Name: "One"}
		st.Members[memberKey("org-1", "user-1")] = Member{Organization: "org-1", User: "user-1"}
		st.Resources["svc-1"] = Resource{ID: "svc-1", Kind: "service", Organization: "org-1", Name: "svc"}
		st.Assignments["a-1"] = Assignment{ID: "a-1", SubjectKind: "user", SubjectID: "user-1", Role: "viewer", ScopeKind: "organization", ScopeID: "org-1", Revision: 1}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(ctx, PartitionIAM, func(st *State) error {
		delete(st.Organizations, "org-1")
		delete(st.Members, memberKey("org-1", "user-1"))
		delete(st.Assignments, "a-1")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var members, assignments int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM krun_members WHERE org_id='org-1'`).Scan(&members); err != nil {
		t.Fatal(err)
	}
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM krun_assignments WHERE scope_id='org-1'`).Scan(&assignments); err != nil {
		t.Fatal(err)
	}
	if members != 0 || assignments != 0 {
		t.Fatalf("orphan rows remain: members=%d assignments=%d", members, assignments)
	}
}

// TestPostgresRetentionPrunesBuildsAndSessions covers the background cleanup:
// expired sessions go, and builds under the per-service cap stay.
func TestPostgresRetentionPrunesBuildsAndSessions(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	old := now().Add(-40 * 24 * time.Hour)
	if err := store.Update(ctx, PartitionWorkers, func(st *State) error {
		for i := 0; i < 5; i++ {
			id := "bld-" + string(rune('a'+i))
			st.Workers.Builds[id] = WorkerBuild{ID: id, Organization: "o", Service: "s", Environment: "production", State: "succeeded", Created: old.Add(time.Duration(i) * time.Minute)}
			st.Workers.BuildPrivate[id] = WorkerBuildPrivate{Actor: WorkerActor{User: "u"}, Log: "log"}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	at := now()
	if _, err := store.pool.Exec(ctx, `UPDATE krun_worker_builds SET created_at = $1`, at.Add(-40*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := store.insertSession(ctx, "t1", Session{User: "u", CSRF: "c", Created: at.Add(-time.Hour), Seen: at.Add(-time.Hour), Absolute: at.Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if err := store.retainExpired(ctx); err != nil {
		t.Fatal(err)
	}
	var sessions int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM krun_sessions`).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if sessions != 0 {
		t.Errorf("expired session not removed: %d", sessions)
	}
	var builds int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM krun_worker_builds`).Scan(&builds); err != nil {
		t.Fatal(err)
	}
	if builds != 5 {
		t.Errorf("builds under the per-service cap must be kept, got %d", builds)
	}
}

// TestPostgresPendingWorkIncludesBuildActor covers the regression where
// pendingWork() loaded builds but not their private row. Authorization then saw
// an empty actor and rejected every build with "Build permission revoked".
func TestPostgresPendingWorkIncludesBuildActor(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	if err := store.Update(context.Background(), PartitionAll, func(st *State) error {
		st.Users["u1"] = User{ID: "u1", Source: "local", Subject: "u1", Status: "active", AuthVersion: 3}
		st.Organizations["o1"] = Organization{ID: "o1", Name: "O"}
		st.Resources["s1"] = Resource{ID: "s1", Kind: "service", Organization: "o1", Revision: 1}
		st.Workers.Services["s1"] = WorkerService{Service: "s1", Organization: "o1", Domains: []string{"internal"}, Environments: map[string]WorkerEnvironment{"production": {Name: "production", Enabled: true, ConfigRevision: 1}}}
		st.Workers.Builds["b1"] = WorkerBuild{ID: "b1", Organization: "o1", Service: "s1", Environment: "production", State: "queued", Created: now()}
		st.Workers.BuildPrivate["b1"] = WorkerBuildPrivate{Actor: WorkerActor{User: "u1", AuthVersion: 3}, Files: map[string]string{"index.js": "x"}}
		st.Workers.Releases["r1"] = WorkerRelease{ID: "r1", Organization: "o1", Service: "s1", Environment: "production", Artifact: "a1", Pool: "default"}
		st.Workers.Artifacts["a1"] = WorkerArtifact{ID: "a1", Organization: "o1", State: "finalized"}
		st.Workers.Operations["op1"] = WorkerOperation{ID: "op1", Kind: "publish", Organization: "o1", Service: "s1", Environment: "production", State: "queued", Release: "r1", Created: now()}
		st.Workers.OperationPrivate["op1"] = WorkerOperationPrivate{Actor: WorkerActor{User: "u1", AuthVersion: 3}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	pending, err := store.pendingWork(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if got := pending.BuildPrivate["b1"].Actor; got.User != "u1" || got.AuthVersion != 3 {
		t.Fatalf("build actor not loaded: %+v", got)
	}
	if len(pending.BuildPrivate["b1"].Files) == 0 {
		t.Fatal("build source files not loaded")
	}
	if _, ok := pending.Releases["r1"]; !ok {
		t.Fatal("release referenced by an in-flight operation not loaded")
	}
	if _, ok := pending.Artifacts["a1"]; !ok {
		t.Fatal("artifact referenced by that release not loaded")
	}
	if got := pending.OperationPrivate["op1"].Actor; got.User != "u1" {
		t.Fatalf("operation actor not loaded: %+v", got)
	}
}

// TestPostgresCommitMarksStepsDone covers the regression where a committed
// operation kept steps 0-2 at "pending", so the console showed "等待执行" for
// steps that had already finished.
func TestPostgresCommitMarksStepsDone(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	steps := []WorkerStep{
		{Name: "artifact", State: "pending"},
		{Name: "candidate", State: "pending"},
		{Name: "commit", State: "pending"},
		{Name: "synchronize", State: "pending"},
	}
	if err := store.Update(context.Background(), PartitionWorkers, func(st *State) error {
		st.Workers.Services["s1"] = WorkerService{Service: "s1", Organization: "o1", Domains: []string{"internal"},
			Environments: map[string]WorkerEnvironment{"production": {Name: "production", Enabled: true, ConfigRevision: 1}}}
		st.Workers.Operations["op1"] = WorkerOperation{ID: "op1", Kind: "publish", Organization: "o1", Service: "s1", Environment: "production", State: "validating", Release: "r1", ExpectedRevision: 0, Steps: steps, Created: now()}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	committed, err := store.commitOperation(ctx, WorkerOperation{ID: "op1", Service: "s1", Environment: "production", Release: "r1", ExpectedRevision: 0})
	if err != nil {
		t.Fatal(err)
	}
	if !committed {
		t.Fatal("operation should have committed")
	}
	st, err := store.Open(ctx, PartitionWorkers)
	if err != nil {
		t.Fatal(err)
	}
	got := st.Workers.Operations["op1"]
	if got.State != "committed" {
		t.Fatalf("state = %q", got.State)
	}
	want := map[string]string{"artifact": "done", "candidate": "done", "commit": "done", "synchronize": "running"}
	for _, step := range got.Steps {
		if want[step.Name] != step.State {
			t.Errorf("step %s = %q, want %q", step.Name, step.State, want[step.Name])
		}
	}
}
