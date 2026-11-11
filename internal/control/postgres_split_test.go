package control

import (
	"context"
	"sync"
	"testing"
	"time"

	appconfig "github.com/krun-dev/krun/internal/config"
)

// TestPostgresBuildClaimIsExclusive covers the replacing of the advisory-lock
// leader election: two replicas racing for the same build must not both win.
func TestPostgresBuildClaimIsExclusive(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	if _, err := postgresTestPool(store).Exec(ctx, `INSERT INTO krun_workers (worker_id, org_id) VALUES ('svc-1','org-1')`); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(ctx, PartitionWorkers, func(st *State) error {
		st.Workers.Builds["bld-1"] = WorkerBuild{ID: "bld-1", Organization: "org-1", Worker: "svc-1", Environment: "production", State: "queued", Created: now()}
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
			claimed, err := store.ClaimBuild(ctx, "bld-1", replica)
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
	var owner string
	winners.Range(func(key, _ any) bool { owner = key.(string); return false })
	if claimed, err := store.ClaimBuild(ctx, "bld-1", owner); err != nil || !claimed {
		t.Fatalf("owner could not refresh its build lease: claimed=%v err=%v", claimed, err)
	}
	if claimed, err := store.ClaimBuild(ctx, "bld-1", "replica-new"); err != nil || claimed {
		t.Fatalf("fresh build lease was stolen: claimed=%v err=%v", claimed, err)
	}
	if _, err := postgresTestPool(store).Exec(ctx, `UPDATE krun_worker_builds SET claimed_at=now()-interval '5 minutes' WHERE build_id='bld-1'`); err != nil {
		t.Fatal(err)
	}
	if claimed, err := store.ClaimBuild(ctx, "bld-1", "replica-new"); err != nil || !claimed {
		t.Fatalf("expired build lease was not taken over: claimed=%v err=%v", claimed, err)
	}
}

func TestPostgresWorkerIdempotencyClaimIsAtomic(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	if _, err := postgresTestPool(store).Exec(ctx, `INSERT INTO krun_workers (worker_id, org_id) VALUES ('svc-1','org-1')`); err != nil {
		t.Fatal(err)
	}
	ready := make(chan struct{}, 2)
	start := make(chan struct{})
	errs := make(chan error, 2)
	for _, id := range []string{"bld-a", "bld-b"} {
		go func(id string) {
			errs <- store.Update(ctx, PartitionWorkers, func(st *State) error {
				st.Workers.Builds[id] = WorkerBuild{ID: id, Organization: "org-1", Worker: "svc-1", Environment: "production", State: "queued", Created: now()}
				st.Workers.Idempotency["same-key"] = WorkerIdempotency{Digest: "same-request", ID: id}
				ready <- struct{}{}
				<-start
				return nil
			})
		}(id)
	}
	<-ready
	<-ready
	close(start)
	succeeded, conflicted := 0, 0
	for range 2 {
		err := <-errs
		if err == nil {
			succeeded++
		} else if apiErr, ok := err.(*APIError); ok && apiErr.Status == 409 {
			conflicted++
		} else {
			t.Fatalf("unexpected mutation error: %v", err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("succeeded=%d conflicted=%d", succeeded, conflicted)
	}
	st, err := store.Open(ctx, PartitionWorkers)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Workers.Builds) != 1 || len(st.Workers.Idempotency) != 1 {
		t.Fatalf("atomic reservation left duplicate targets: builds=%d idempotency=%d", len(st.Workers.Builds), len(st.Workers.Idempotency))
	}
}

func TestPostgresWorkerRepositoryRevisionCASIsExclusive(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	if err := store.Update(ctx, PartitionWorkers, func(st *State) error {
		st.Workers.Workers["svc-repository-cas"] = Worker{
			Worker: "svc-repository-cas", Organization: "org-1", Domains: []string{"internal"},
			Environments: map[string]WorkerEnvironment{appconfig.WorkerBaseline: {
				Name: appconfig.WorkerBaseline, Enabled: true, ConfigRevision: 1, Variables: map[string]any{},
			}},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	ready := make(chan struct{}, 2)
	start := make(chan struct{})
	errs := make(chan error, 2)
	for _, repository := range []string{"team/alpha", "team/bravo"} {
		go func(repository string) {
			errs <- store.Update(ctx, PartitionWorkers, func(st *State) error {
				service := st.Workers.Workers["svc-repository-cas"]
				if service.RepositoryRevision != 0 {
					return problem(409, "REVISION_CONFLICT", "test did not load the shared pre-image")
				}
				service.Repository = &WorkerRepositorySource{ProviderID: "github", Repository: repository, Ref: "main"}
				service.RepositoryRevision++
				st.Workers.Workers[service.Worker] = service
				ready <- struct{}{}
				<-start
				return nil
			})
		}(repository)
	}
	<-ready
	<-ready
	close(start)
	assertOneWorkerCASWinner(t, errs)
	st, err := store.Open(ctx, PartitionWorkers)
	if err != nil {
		t.Fatal(err)
	}
	service := st.Workers.Workers["svc-repository-cas"]
	if service.RepositoryRevision != 1 || service.Repository == nil {
		t.Fatalf("repository CAS result = %+v", service)
	}
}

func TestPostgresWorkerEnvironmentConfigCASIsExclusive(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	if err := store.Update(ctx, PartitionWorkers, func(st *State) error {
		st.Workers.Workers["svc-environment-cas"] = Worker{
			Worker: "svc-environment-cas", Organization: "org-1", Domains: []string{"internal"},
			Environments: map[string]WorkerEnvironment{appconfig.WorkerBaseline: {
				Name: appconfig.WorkerBaseline, Enabled: true, ConfigRevision: 1, Variables: map[string]any{},
			}},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	ready := make(chan struct{}, 2)
	start := make(chan struct{})
	errs := make(chan error, 2)
	for _, value := range []string{"alpha", "bravo"} {
		go func(value string) {
			errs <- store.Update(ctx, PartitionWorkers, func(st *State) error {
				service := st.Workers.Workers["svc-environment-cas"]
				environment := service.Environments[appconfig.WorkerBaseline]
				if environment.ConfigRevision != 1 {
					return problem(409, "REVISION_CONFLICT", "test did not load the shared pre-image")
				}
				environment.Variables = map[string]any{"VALUE": value}
				environment.ConfigRevision++
				service.Environments[appconfig.WorkerBaseline] = environment
				st.Workers.Workers[service.Worker] = service
				ready <- struct{}{}
				<-start
				return nil
			})
		}(value)
	}
	<-ready
	<-ready
	close(start)
	assertOneWorkerCASWinner(t, errs)
	st, err := store.Open(ctx, PartitionWorkers)
	if err != nil {
		t.Fatal(err)
	}
	environment := st.Workers.Workers["svc-environment-cas"].Environments[appconfig.WorkerBaseline]
	if environment.ConfigRevision != 2 || (environment.Variables["VALUE"] != "alpha" && environment.Variables["VALUE"] != "bravo") {
		t.Fatalf("environment CAS result = %+v", environment)
	}
}

func TestPostgresWorkerDomainsCASIsExclusive(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	if err := store.Update(ctx, PartitionWorkers, func(st *State) error {
		st.Workers.Workers["svc-domains-cas"] = Worker{
			Worker: "svc-domains-cas", Organization: "org-1", Domains: []string{"internal"},
			Environments: map[string]WorkerEnvironment{appconfig.WorkerBaseline: {
				Name: appconfig.WorkerBaseline, Enabled: true, ConfigRevision: 1, Variables: map[string]any{},
			}},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	beforeGeneration := workerDirtyGenerationForTest(t, store)
	ready := make(chan struct{}, 2)
	start := make(chan struct{})
	errs := make(chan error, 2)
	for _, domain := range []string{"alpha", "bravo"} {
		go func(domain string) {
			errs <- store.Update(ctx, PartitionWorkers, func(st *State) error {
				service := st.Workers.Workers["svc-domains-cas"]
				if service.DomainsRevision != 0 {
					return problem(409, "REVISION_CONFLICT", "test did not load the shared pre-image")
				}
				service.Domains = []string{domain}
				service.DomainsRevision++
				st.Workers.Workers[service.Worker] = service
				ready <- struct{}{}
				<-start
				return nil
			})
		}(domain)
	}
	<-ready
	<-ready
	close(start)
	assertOneWorkerCASWinner(t, errs)
	st, err := store.Open(ctx, PartitionWorkers)
	if err != nil {
		t.Fatal(err)
	}
	service := st.Workers.Workers["svc-domains-cas"]
	if service.DomainsRevision != 1 || len(service.Domains) != 1 || service.Domains[0] != "alpha" && service.Domains[0] != "bravo" {
		t.Fatalf("domain CAS result = %+v", service)
	}
	if generation := workerDirtyGenerationForTest(t, store); generation <= beforeGeneration {
		t.Fatalf("domain topology generation=%d, want %d", generation, beforeGeneration+1)
	}
}

func TestPostgresWorkerPreviewSetCASPreventsConcurrentLimitOverflow(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	if err := store.Update(ctx, PartitionWorkers, func(st *State) error {
		st.Workers.Workers["svc-preview-cas"] = Worker{
			Worker: "svc-preview-cas", Organization: "org-1", Domains: []string{"internal"},
			Environments: map[string]WorkerEnvironment{appconfig.WorkerBaseline: {
				Name: appconfig.WorkerBaseline, Enabled: true, ConfigRevision: 1, Variables: map[string]any{},
			}},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	beforeGeneration := workerDirtyGenerationForTest(t, store)
	ready := make(chan struct{}, 2)
	start := make(chan struct{})
	errs := make(chan error, 2)
	for _, preview := range []string{"alpha", "bravo"} {
		go func(preview string) {
			errs <- store.Update(ctx, PartitionWorkers, func(st *State) error {
				service := st.Workers.Workers["svc-preview-cas"]
				// This models preview_limit=1: both replicas observe zero slots
				// before either transaction commits.
				if previewCount(service.Environments) != 0 {
					return problem(409, "ENVIRONMENT_LIMIT", "test did not load the shared pre-image")
				}
				service.Environments[preview] = WorkerEnvironment{
					Name: preview, Enabled: true, ConfigRevision: 1, Variables: map[string]any{},
				}
				service.PreviewRevision++
				st.Workers.Workers[service.Worker] = service
				ready <- struct{}{}
				<-start
				return nil
			})
		}(preview)
	}
	<-ready
	<-ready
	close(start)
	assertOneWorkerCASWinner(t, errs)
	st, err := store.Open(ctx, PartitionWorkers)
	if err != nil {
		t.Fatal(err)
	}
	service := st.Workers.Workers["svc-preview-cas"]
	if service.PreviewRevision != 1 || previewCount(service.Environments) != 1 {
		t.Fatalf("preview CAS result = %+v", service)
	}
	if generation := workerDirtyGenerationForTest(t, store); generation <= beforeGeneration {
		t.Fatalf("preview topology generation=%d, want %d", generation, beforeGeneration+1)
	}
}

func workerDirtyGenerationForTest(t *testing.T, store *Store) int64 {
	t.Helper()
	var generation int64
	if err := postgresTestPool(store).QueryRow(context.Background(), `SELECT last_value FROM worker_dirty_seq`).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	return generation
}

func assertOneWorkerCASWinner(t *testing.T, errs <-chan error) {
	t.Helper()
	succeeded, conflicted := 0, 0
	for range 2 {
		err := <-errs
		if err == nil {
			succeeded++
		} else if apiErr, ok := err.(*APIError); ok && apiErr.Code == "REVISION_CONFLICT" {
			conflicted++
		} else {
			t.Fatalf("unexpected mutation error: %v", err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("succeeded=%d conflicted=%d", succeeded, conflicted)
	}
}

func TestPostgresConfigRevisionCASIsExclusive(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	if _, err := postgresTestPool(store).Exec(ctx, `INSERT INTO worker_config_dirty(worker_id) VALUES('site-cas')`); err != nil {
		t.Fatal(err)
	}
	dirty, err := store.DirtyWorkers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		won bool
		err error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	for range 2 {
		go func() {
			<-start
			_, won, err := store.AppendConfigDelta(ctx, 0, dirty, []workerConfigOp{{Type: "route_upsert", Pool: "default", Host: "site.workers.test", Route: &workerConfigRoute{Worker: "site-cas", Host: "site.workers.test", Pool: "default"}}})
			results <- result{won, err}
		}()
	}
	close(start)
	winners := 0
	for range 2 {
		r := <-results
		if r.err != nil {
			t.Fatal(r.err)
		}
		if r.won {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("CAS winners=%d", winners)
	}
	head, _, err := store.ConfigState(ctx)
	if err != nil || head != 1 {
		t.Fatalf("head=%d err=%v", head, err)
	}
}

func TestPostgresOperationClaimIsExclusive(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	if err := store.Update(ctx, PartitionWorkers, func(st *State) error {
		st.Workers.Operations["op-1"] = WorkerOperation{ID: "op-1", Kind: "publish", Organization: "org-1", Worker: "svc-1", Environment: "production", State: "queued", Created: now()}
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
			_, claimed, err := store.ClaimOperation(ctx, "op-1", replica)
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
	_, claimed, err := store.ClaimOperation(ctx, "op-1", "replica-c")
	if err != nil {
		t.Fatal(err)
	}
	if claimed {
		t.Error("a fresh claim must not be stolen")
	}
	if _, err := postgresTestPool(store).Exec(ctx, `UPDATE krun_worker_operations
		SET claimed_at=now()-interval '5 minutes',reconcile_after=now() WHERE operation_id='op-1'`); err != nil {
		t.Fatal(err)
	}
	_, claimed, err = store.ClaimOperation(ctx, "op-1", "replica-c")
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
		st.Workers.Workers["svc-1"] = Worker{
			Worker: "svc-1", Organization: "org-1", Domains: []string{"internal"},
			Environments: map[string]WorkerEnvironment{"production": {Name: "production", Enabled: true, Revision: 3, ConfigRevision: 1}},
		}
		st.Workers.Operations["op-1"] = WorkerOperation{ID: "op-1", Kind: "publish", Organization: "org-1", Worker: "svc-1", Environment: "production", State: "validating", Version: "rel-1", ExpectedRevision: 2, Created: now()}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	claim, owned, err := store.ClaimOperation(ctx, "op-1", "replica")
	if err != nil || !owned {
		t.Fatalf("claim: owned=%t err=%v", owned, err)
	}
	committed, err := store.CommitOperation(ctx, claim)
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
	if got := st.Workers.Workers["svc-1"].Environments["production"].Version; got != "" {
		t.Errorf("version = %q, want it untouched", got)
	}
}

// TestPostgresSessionTouchIsThrottled proves an authenticated read does not
// become a write on every request.
func TestPostgresSessionTouchIsThrottled(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	at := now()
	if err := store.InsertSession(ctx, "token-1", Session{User: "user-1", CSRF: "csrf", AuthVersion: 1, Created: at, Seen: at, Absolute: at.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	var before time.Time
	if err := postgresTestPool(store).QueryRow(ctx, `SELECT seen_at FROM krun_sessions`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	// Inside the throttle window nothing changes.
	if err := store.TouchSession(ctx, digest("token-1")); err != nil {
		t.Fatal(err)
	}
	var after time.Time
	if err := postgresTestPool(store).QueryRow(ctx, `SELECT seen_at FROM krun_sessions`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if !after.Equal(before) {
		t.Fatal("touch inside the throttle window must not write")
	}
	// An aged row is refreshed.
	var aged time.Time
	if err := postgresTestPool(store).QueryRow(ctx, `UPDATE krun_sessions SET seen_at = now() - interval '5 minutes' RETURNING seen_at`).Scan(&aged); err != nil {
		t.Fatal(err)
	}
	if err := store.TouchSession(ctx, digest("token-1")); err != nil {
		t.Fatal(err)
	}
	var refreshed time.Time
	if err := postgresTestPool(store).QueryRow(ctx, `SELECT seen_at FROM krun_sessions`).Scan(&refreshed); err != nil {
		t.Fatal(err)
	}
	if !refreshed.After(aged) {
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
		st.Resources["svc-1"] = Resource{ID: "svc-1", Kind: "worker", Organization: "org-1", Name: "svc"}
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
	if err := postgresTestPool(store).QueryRow(ctx, `SELECT count(*) FROM krun_members WHERE org_id='org-1'`).Scan(&members); err != nil {
		t.Fatal(err)
	}
	if err := postgresTestPool(store).QueryRow(ctx, `SELECT count(*) FROM krun_assignments WHERE scope_id='org-1'`).Scan(&assignments); err != nil {
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
	if err := store.Update(ctx, PartitionIAM, func(st *State) error {
		st.Organizations["o"] = Organization{ID: "o", Name: "Org"}
		st.Resources["s"] = Resource{ID: "s", Kind: "worker", Organization: "o", Name: "Worker"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(ctx, PartitionWorkers, func(st *State) error {
		for i := 0; i < 5; i++ {
			id := "bld-" + string(rune('a'+i))
			st.Workers.Builds[id] = WorkerBuild{ID: id, Organization: "o", Worker: "s", Environment: "production", State: "succeeded", Created: old.Add(time.Duration(i) * time.Minute)}
			st.Workers.BuildPrivate[id] = WorkerBuildPrivate{Actor: WorkerActor{User: "u"}, LogKey: "build-logs/log"}
		}
		st.Workers.Artifacts["art-old"] = WorkerArtifact{ID: "art-old", Organization: "o", State: "finalized", ArchiveDigest: "archive-old", ObjectKey: "v1/o/old.tar.gz", StorageLocation: "test", Bucket: "test", Created: old}
		st.Workers.Versions["rel-old"] = WorkerVersion{ID: "rel-old", Organization: "o", Worker: "s", Environment: "production", Artifact: "art-old", ArchiveDigest: "archive-old", Created: old}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	at := now()
	if _, err := postgresTestPool(store).Exec(ctx, `UPDATE krun_worker_builds SET created_at = $1`, at.Add(-40*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := store.InsertSession(ctx, "t1", Session{User: "u", CSRF: "c", Created: at.Add(-time.Hour), Seen: at.Add(-time.Hour), Absolute: at.Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if err := store.RetainExpired(ctx); err != nil {
		t.Fatal(err)
	}
	var sessions int
	if err := postgresTestPool(store).QueryRow(ctx, `SELECT count(*) FROM krun_sessions`).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if sessions != 0 {
		t.Errorf("expired session not removed: %d", sessions)
	}
	var builds int
	if err := postgresTestPool(store).QueryRow(ctx, `SELECT count(*) FROM krun_worker_builds`).Scan(&builds); err != nil {
		t.Fatal(err)
	}
	if builds != 5 {
		t.Errorf("builds under the per-service cap must be kept, got %d", builds)
	}
	var versions, artifacts int
	if err := postgresTestPool(store).QueryRow(ctx, `SELECT count(*) FROM krun_worker_versions WHERE version_id='rel-old'`).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if err := postgresTestPool(store).QueryRow(ctx, `SELECT count(*) FROM krun_worker_artifacts WHERE artifact_id='art-old'`).Scan(&artifacts); err != nil {
		t.Fatal(err)
	}
	if versions != 0 || artifacts != 1 {
		t.Fatalf("version/artifact GC handoff broken: versions=%d artifacts=%d", versions, artifacts)
	}
	candidates, err := store.GarbageArtifacts(ctx, at.Add(-workerArtifactRetain), 10)
	if err != nil || len(candidates) != 1 || candidates[0].ID != "art-old" {
		t.Fatalf("artifact was not retained for object-store GC: %+v err=%v", candidates, err)
	}
}

func TestPostgresRetentionReconcilesDeletedWorker(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	if err := store.Update(ctx, PartitionIAM, func(st *State) error {
		st.Organizations["orphan-org"] = Organization{ID: "orphan-org", Name: "Org"}
		st.Resources["orphan-service"] = Resource{ID: "orphan-service", Kind: "worker", Organization: "orphan-org", Name: "Worker"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(ctx, PartitionWorkers, func(st *State) error {
		st.Workers.Workers["orphan-service"] = Worker{Worker: "orphan-service", Organization: "orphan-org", Environments: map[string]WorkerEnvironment{"production": {Name: "production", Enabled: true}}}
		st.Workers.Builds["orphan-build"] = WorkerBuild{ID: "orphan-build", Organization: "orphan-org", Worker: "orphan-service", Environment: "production", State: "failed", Created: now()}
		st.Workers.BuildPrivate["orphan-build"] = WorkerBuildPrivate{Actor: WorkerActor{User: "u"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(ctx, PartitionIAM, func(st *State) error {
		delete(st.Resources, "orphan-service")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.RetainExpired(ctx); err != nil {
		t.Fatal(err)
	}
	var services, builds, private int
	if err := postgresTestPool(store).QueryRow(ctx, `SELECT count(*) FROM krun_workers WHERE worker_id='orphan-service'`).Scan(&services); err != nil {
		t.Fatal(err)
	}
	if err := postgresTestPool(store).QueryRow(ctx, `SELECT count(*) FROM krun_worker_builds WHERE worker_id='orphan-service'`).Scan(&builds); err != nil {
		t.Fatal(err)
	}
	if err := postgresTestPool(store).QueryRow(ctx, `SELECT count(*) FROM krun_worker_build_private WHERE build_id='orphan-build'`).Scan(&private); err != nil {
		t.Fatal(err)
	}
	if services != 0 || builds != 0 || private != 0 {
		t.Fatalf("orphan Workers rows remain: services=%d builds=%d private=%d", services, builds, private)
	}
}

func TestPostgresArtifactGCWaitsForRetainedBuild(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	old := now().Add(-workerArtifactRetain - time.Hour)
	if err := store.Update(ctx, PartitionWorkers, func(st *State) error {
		st.Workers.Artifacts["art-held"] = WorkerArtifact{ID: "art-held", Organization: "org", State: "finalized", ArchiveDigest: "digest", ObjectKey: "v1/org/digest/bundle.tar.gz", StorageLocation: "test", Bucket: "test", Created: old}
		st.Workers.Builds["build-held"] = WorkerBuild{ID: "build-held", Organization: "org", Worker: "worker", Environment: "production", State: "succeeded", Artifact: "art-held", Created: old}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	candidates, err := store.GarbageArtifacts(ctx, now().Add(-workerArtifactRetain), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 0 {
		t.Fatalf("artifact referenced by a retained build became collectible: %+v", candidates)
	}
	if _, err := postgresTestPool(store).Exec(ctx, `DELETE FROM krun_worker_builds WHERE build_id='build-held'`); err != nil {
		t.Fatal(err)
	}
	candidates, err = store.GarbageArtifacts(ctx, now().Add(-workerArtifactRetain), 10)
	if err != nil || len(candidates) != 1 || candidates[0].ID != "art-held" {
		t.Fatalf("artifact did not become collectible after build removal: %+v err=%v", candidates, err)
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
		st.Resources["s1"] = Resource{ID: "s1", Kind: "worker", Organization: "o1", Revision: 1}
		st.Workers.Workers["s1"] = Worker{Worker: "s1", Organization: "o1", Domains: []string{"internal"}, Environments: map[string]WorkerEnvironment{"production": {Name: "production", Enabled: true, ConfigRevision: 1}}}
		st.Workers.Builds["b1"] = WorkerBuild{ID: "b1", Organization: "o1", Worker: "s1", Environment: "production", State: "queued", Created: now()}
		st.Workers.BuildPrivate["b1"] = WorkerBuildPrivate{Actor: WorkerActor{User: "u1", AuthVersion: 3}, Files: map[string]string{"index.js": "x"}}
		st.Workers.Versions["r1"] = WorkerVersion{ID: "r1", Organization: "o1", Worker: "s1", Environment: "production", Artifact: "a1"}
		st.Workers.Artifacts["a1"] = WorkerArtifact{ID: "a1", Organization: "o1", State: "finalized"}
		st.Workers.Operations["op1"] = WorkerOperation{ID: "op1", Kind: "publish", Organization: "o1", Worker: "s1", Environment: "production", State: "queued", Version: "r1", Created: now()}
		st.Workers.OperationPrivate["op1"] = WorkerOperationPrivate{Actor: WorkerActor{User: "u1", AuthVersion: 3}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	pending, err := store.PendingWork(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if got := pending.BuildPrivate["b1"].Actor; got.User != "u1" || got.AuthVersion != 3 {
		t.Fatalf("build actor not loaded: %+v", got)
	}
	if len(pending.BuildPrivate["b1"].Files) == 0 {
		t.Fatal("build source files not loaded")
	}
	if _, ok := pending.Versions["r1"]; !ok {
		t.Fatal("version referenced by an in-flight operation not loaded")
	}
	if _, ok := pending.Artifacts["a1"]; !ok {
		t.Fatal("artifact referenced by that version not loaded")
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
		st.Workers.Workers["s1"] = Worker{Worker: "s1", Organization: "o1", Domains: []string{"internal"},
			Environments: map[string]WorkerEnvironment{"production": {Name: "production", Enabled: true, ConfigRevision: 1}}}
		st.Workers.Operations["op1"] = WorkerOperation{ID: "op1", Kind: "publish", Organization: "o1", Worker: "s1", Environment: "production", State: "validating", Version: "r1", ExpectedRevision: 0, Steps: steps, Created: now()}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	claim, owned, err := store.ClaimOperation(ctx, "op1", "replica")
	if err != nil || !owned {
		t.Fatalf("claim: owned=%t err=%v", owned, err)
	}
	committed, err := store.CommitOperation(ctx, claim)
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
