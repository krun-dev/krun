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
	if _, err := store.pool.Exec(ctx, `INSERT INTO krun_worker_services (service_id, org_id) VALUES ('svc-1','org-1')`); err != nil {
		t.Fatal(err)
	}
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
	var owner string
	winners.Range(func(key, _ any) bool { owner = key.(string); return false })
	if claimed, err := store.claimBuild(ctx, "bld-1", owner); err != nil || !claimed {
		t.Fatalf("owner could not refresh its build lease: claimed=%v err=%v", claimed, err)
	}
	if claimed, err := store.claimBuild(ctx, "bld-1", "replica-new"); err != nil || claimed {
		t.Fatalf("fresh build lease was stolen: claimed=%v err=%v", claimed, err)
	}
	if _, err := store.pool.Exec(ctx, `UPDATE krun_worker_builds SET claimed_at=now()-interval '5 minutes' WHERE build_id='bld-1'`); err != nil {
		t.Fatal(err)
	}
	if claimed, err := store.claimBuild(ctx, "bld-1", "replica-new"); err != nil || !claimed {
		t.Fatalf("expired build lease was not taken over: claimed=%v err=%v", claimed, err)
	}
}

func TestPostgresWorkerIdempotencyClaimIsAtomic(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	if _, err := store.pool.Exec(ctx, `INSERT INTO krun_worker_services (service_id, org_id) VALUES ('svc-1','org-1')`); err != nil {
		t.Fatal(err)
	}
	ready := make(chan struct{}, 2)
	start := make(chan struct{})
	errs := make(chan error, 2)
	for _, id := range []string{"bld-a", "bld-b"} {
		go func(id string) {
			errs <- store.Update(ctx, PartitionWorkers, func(st *State) error {
				st.Workers.Builds[id] = WorkerBuild{ID: id, Organization: "org-1", Service: "svc-1", Environment: "production", State: "queued", Created: now()}
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
		st.Workers.Services["svc-repository-cas"] = WorkerService{
			Service: "svc-repository-cas", Organization: "org-1", Domains: []string{"internal"},
			Environments: map[string]WorkerEnvironment{workerBaseline: {
				Name: workerBaseline, Enabled: true, ConfigRevision: 1, Variables: map[string]string{},
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
				service := st.Workers.Services["svc-repository-cas"]
				if service.RepositoryRevision != 0 {
					return problem(409, "REVISION_CONFLICT", "test did not load the shared pre-image")
				}
				service.Repository = &WorkerRepositorySource{ProviderID: "github", Repository: repository, Ref: "main", Entrypoint: "index.ts"}
				service.RepositoryRevision++
				st.Workers.Services[service.Service] = service
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
	service := st.Workers.Services["svc-repository-cas"]
	if service.RepositoryRevision != 1 || service.Repository == nil {
		t.Fatalf("repository CAS result = %+v", service)
	}
}

func TestPostgresWorkerEnvironmentConfigCASIsExclusive(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	if err := store.Update(ctx, PartitionWorkers, func(st *State) error {
		st.Workers.Services["svc-environment-cas"] = WorkerService{
			Service: "svc-environment-cas", Organization: "org-1", Domains: []string{"internal"},
			Environments: map[string]WorkerEnvironment{workerBaseline: {
				Name: workerBaseline, Enabled: true, ConfigRevision: 1, Variables: map[string]string{},
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
				service := st.Workers.Services["svc-environment-cas"]
				environment := service.Environments[workerBaseline]
				if environment.ConfigRevision != 1 {
					return problem(409, "REVISION_CONFLICT", "test did not load the shared pre-image")
				}
				environment.Variables = map[string]string{"VALUE": value}
				environment.ConfigRevision++
				service.Environments[workerBaseline] = environment
				st.Workers.Services[service.Service] = service
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
	environment := st.Workers.Services["svc-environment-cas"].Environments[workerBaseline]
	if environment.ConfigRevision != 2 || (environment.Variables["VALUE"] != "alpha" && environment.Variables["VALUE"] != "bravo") {
		t.Fatalf("environment CAS result = %+v", environment)
	}
}

func TestPostgresWorkerDomainsCASIsExclusive(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	if err := store.Update(ctx, PartitionWorkers, func(st *State) error {
		st.Workers.Services["svc-domains-cas"] = WorkerService{
			Service: "svc-domains-cas", Organization: "org-1", Domains: []string{"internal"},
			Environments: map[string]WorkerEnvironment{workerBaseline: {
				Name: workerBaseline, Enabled: true, ConfigRevision: 1, Variables: map[string]string{},
			}},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	beforeGeneration := workerTopologyGenerationForTest(t, store)
	ready := make(chan struct{}, 2)
	start := make(chan struct{})
	errs := make(chan error, 2)
	for _, domain := range []string{"alpha", "bravo"} {
		go func(domain string) {
			errs <- store.Update(ctx, PartitionWorkers, func(st *State) error {
				service := st.Workers.Services["svc-domains-cas"]
				if service.DomainsRevision != 0 {
					return problem(409, "REVISION_CONFLICT", "test did not load the shared pre-image")
				}
				service.Domains = []string{domain}
				service.DomainsRevision++
				st.Workers.Services[service.Service] = service
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
	service := st.Workers.Services["svc-domains-cas"]
	if service.DomainsRevision != 1 || len(service.Domains) != 1 || service.Domains[0] != "alpha" && service.Domains[0] != "bravo" {
		t.Fatalf("domain CAS result = %+v", service)
	}
	if generation := workerTopologyGenerationForTest(t, store); generation != beforeGeneration+1 {
		t.Fatalf("domain topology generation=%d, want %d", generation, beforeGeneration+1)
	}
}

func TestPostgresWorkerPreviewSetCASPreventsConcurrentLimitOverflow(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	if err := store.Update(ctx, PartitionWorkers, func(st *State) error {
		st.Workers.Services["svc-preview-cas"] = WorkerService{
			Service: "svc-preview-cas", Organization: "org-1", Domains: []string{"internal"},
			Environments: map[string]WorkerEnvironment{workerBaseline: {
				Name: workerBaseline, Enabled: true, ConfigRevision: 1, Variables: map[string]string{},
			}},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	beforeGeneration := workerTopologyGenerationForTest(t, store)
	ready := make(chan struct{}, 2)
	start := make(chan struct{})
	errs := make(chan error, 2)
	for _, preview := range []string{"alpha", "bravo"} {
		go func(preview string) {
			errs <- store.Update(ctx, PartitionWorkers, func(st *State) error {
				service := st.Workers.Services["svc-preview-cas"]
				// This models preview_limit=1: both replicas observe zero slots
				// before either transaction commits.
				if previewCount(service.Environments) != 0 {
					return problem(409, "ENVIRONMENT_LIMIT", "test did not load the shared pre-image")
				}
				service.Environments[preview] = WorkerEnvironment{
					Name: preview, Enabled: true, ConfigRevision: 1, Variables: map[string]string{},
				}
				service.PreviewRevision++
				st.Workers.Services[service.Service] = service
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
	service := st.Workers.Services["svc-preview-cas"]
	if service.PreviewRevision != 1 || previewCount(service.Environments) != 1 {
		t.Fatalf("preview CAS result = %+v", service)
	}
	if generation := workerTopologyGenerationForTest(t, store); generation != beforeGeneration+1 {
		t.Fatalf("preview topology generation=%d, want %d", generation, beforeGeneration+1)
	}
}

func workerTopologyGenerationForTest(t *testing.T, store *Store) int64 {
	t.Helper()
	var generation int64
	if err := store.pool.QueryRow(context.Background(), `SELECT value::bigint FROM krun_meta WHERE key='worker_topology_generation'`).Scan(&generation); err != nil {
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
	if _, err := store.pool.Exec(ctx, `DELETE FROM worker_config_revisions`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `DELETE FROM krun_meta WHERE key='worker_config_head'`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = store.pool.Exec(context.Background(), `DELETE FROM worker_config_revisions`)
		_, _ = store.pool.Exec(context.Background(), `DELETE FROM krun_meta WHERE key='worker_config_head'`)
	})
	type result struct {
		revision int64
		appended bool
		err      error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	for _, digest := range []string{"publisher-a", "publisher-b"} {
		go func(digest string) {
			<-start
			revision, _, appended, err := store.AppendConfigRevisionCAS(ctx, 0, digest, []byte(`{"routes":[]}`), []byte(`[]`), 10)
			results <- result{revision: revision, appended: appended, err: err}
		}(digest)
	}
	close(start)
	winners := 0
	for range 2 {
		got := <-results
		if got.err != nil {
			t.Fatal(got.err)
		}
		if got.revision != 1 {
			t.Fatalf("revision=%d, want durable head 1", got.revision)
		}
		if got.appended {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("CAS winners=%d, want 1", winners)
	}
	var rows int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM worker_config_revisions`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("config revisions=%d err=%v", rows, err)
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
	var aged time.Time
	if err := store.pool.QueryRow(ctx, `UPDATE krun_sessions SET seen_at = now() - interval '5 minutes' RETURNING seen_at`).Scan(&aged); err != nil {
		t.Fatal(err)
	}
	if err := store.touchSession(ctx, digest("token-1")); err != nil {
		t.Fatal(err)
	}
	var refreshed time.Time
	if err := store.pool.QueryRow(ctx, `SELECT seen_at FROM krun_sessions`).Scan(&refreshed); err != nil {
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
	if err := store.Update(ctx, PartitionIAM, func(st *State) error {
		st.Organizations["o"] = Organization{ID: "o", Name: "Org"}
		st.Resources["s"] = Resource{ID: "s", Kind: "service", Organization: "o", Name: "Service"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(ctx, PartitionWorkers, func(st *State) error {
		for i := 0; i < 5; i++ {
			id := "bld-" + string(rune('a'+i))
			st.Workers.Builds[id] = WorkerBuild{ID: id, Organization: "o", Service: "s", Environment: "production", State: "succeeded", Created: old.Add(time.Duration(i) * time.Minute)}
			st.Workers.BuildPrivate[id] = WorkerBuildPrivate{Actor: WorkerActor{User: "u"}, Log: "log"}
		}
		st.Workers.Artifacts["art-old"] = WorkerArtifact{ID: "art-old", Organization: "o", State: "finalized", ArchiveDigest: "archive-old", ObjectKey: "v1/o/old.tar.gz", StorageLocation: "test", Bucket: "test", Created: old}
		st.Workers.Releases["rel-old"] = WorkerRelease{ID: "rel-old", Organization: "o", Service: "s", Environment: "production", Artifact: "art-old", Pool: "default", Created: old}
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
	var releases, artifacts int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM krun_worker_releases WHERE release_id='rel-old'`).Scan(&releases); err != nil {
		t.Fatal(err)
	}
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM krun_worker_artifacts WHERE artifact_id='art-old'`).Scan(&artifacts); err != nil {
		t.Fatal(err)
	}
	if releases != 0 || artifacts != 1 {
		t.Fatalf("release/artifact GC handoff broken: releases=%d artifacts=%d", releases, artifacts)
	}
	candidates, err := store.garbageArtifacts(ctx, at.Add(-workerArtifactRetain), 10)
	if err != nil || len(candidates) != 1 || candidates[0].ID != "art-old" {
		t.Fatalf("artifact was not retained for object-store GC: %+v err=%v", candidates, err)
	}
}

func TestPostgresRetentionReconcilesDeletedWorkerService(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	if err := store.Update(ctx, PartitionIAM, func(st *State) error {
		st.Organizations["orphan-org"] = Organization{ID: "orphan-org", Name: "Org"}
		st.Resources["orphan-service"] = Resource{ID: "orphan-service", Kind: "service", Organization: "orphan-org", Name: "Service"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(ctx, PartitionWorkers, func(st *State) error {
		st.Workers.Services["orphan-service"] = WorkerService{Service: "orphan-service", Organization: "orphan-org", Environments: map[string]WorkerEnvironment{"production": {Name: "production", Enabled: true}}}
		st.Workers.Builds["orphan-build"] = WorkerBuild{ID: "orphan-build", Organization: "orphan-org", Service: "orphan-service", Environment: "production", State: "failed", Created: now()}
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
	if err := store.retainExpired(ctx); err != nil {
		t.Fatal(err)
	}
	var services, builds, private int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM krun_worker_services WHERE service_id='orphan-service'`).Scan(&services); err != nil {
		t.Fatal(err)
	}
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM krun_worker_builds WHERE service_id='orphan-service'`).Scan(&builds); err != nil {
		t.Fatal(err)
	}
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM krun_worker_build_private WHERE build_id='orphan-build'`).Scan(&private); err != nil {
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
		st.Workers.Builds["build-held"] = WorkerBuild{ID: "build-held", Organization: "org", Service: "service", Environment: "production", State: "succeeded", Artifact: "art-held", Created: old}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	candidates, err := store.garbageArtifacts(ctx, now().Add(-workerArtifactRetain), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 0 {
		t.Fatalf("artifact referenced by a retained build became collectible: %+v", candidates)
	}
	if _, err := store.pool.Exec(ctx, `DELETE FROM krun_worker_builds WHERE build_id='build-held'`); err != nil {
		t.Fatal(err)
	}
	candidates, err = store.garbageArtifacts(ctx, now().Add(-workerArtifactRetain), 10)
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
