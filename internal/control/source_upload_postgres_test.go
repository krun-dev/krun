package control

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPostgresSourceUploadReservationDefeatsStaleGCClaim(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	if err := store.Update(ctx, PartitionWorkers, func(st *State) error {
		st.Workers.Services["source-service"] = WorkerService{
			Service: "source-service", Organization: "source-org",
			Environments: map[string]WorkerEnvironment{workerBaseline: {
				Name: workerBaseline, Enabled: true, ConfigRevision: 1, Variables: map[string]string{},
			}},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	key := workerSourceUploadKey("source-org", "src-reservation")
	if err := store.issueWorkerSourceUpload(ctx, key, "source-org"); err != nil {
		t.Fatal(err)
	}
	// Select using a cutoff just ahead of the real retention boundary. The
	// reservation is still currently valid, which deterministically models a
	// GC listing that becomes stale while a build accepts the capability.
	if _, err := store.pool.Exec(ctx, `UPDATE krun_worker_source_uploads SET created_at=now()-interval '23 hours' WHERE object_key=$1`, key); err != nil {
		t.Fatal(err)
	}
	candidates, err := store.garbageWorkerSourceUploads(ctx, now().Add(-22*time.Hour), 10)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("candidates=%+v err=%v", candidates, err)
	}
	staleRevision := candidates[0].UseRevision
	if err := store.Update(ctx, PartitionWorkers, func(st *State) error {
		st.Workers.Builds["source-build"] = WorkerBuild{
			ID: "source-build", Organization: "source-org", Service: "source-service",
			Environment: workerBaseline, State: "queued", Entrypoint: "index.ts", Created: now(),
		}
		st.Workers.BuildPrivate["source-build"] = WorkerBuildPrivate{SourceKey: key}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if claimed, err := store.claimGarbageWorkerSourceUpload(ctx, key, "gc-stale", staleRevision); err != nil || claimed {
		t.Fatalf("stale GC claim won after build reservation: claimed=%v err=%v", claimed, err)
	}
	var state, buildID string
	var revision int64
	if err := store.pool.QueryRow(ctx, `SELECT state, build_id, use_revision FROM krun_worker_source_uploads WHERE object_key=$1`, key).Scan(&state, &buildID, &revision); err != nil {
		t.Fatal(err)
	}
	if state != "reserved" || buildID != "source-build" || revision <= staleRevision {
		t.Fatalf("reservation state=%s build=%s revision=%d stale=%d", state, buildID, revision, staleRevision)
	}

	// The same upload capability is one-shot; a second build cannot share it.
	err = store.Update(ctx, PartitionWorkers, func(st *State) error {
		st.Workers.Builds["source-build-reuse"] = WorkerBuild{
			ID: "source-build-reuse", Organization: "source-org", Service: "source-service",
			Environment: workerBaseline, State: "queued", Entrypoint: "index.ts", Created: now(),
		}
		st.Workers.BuildPrivate["source-build-reuse"] = WorkerBuildPrivate{SourceKey: key}
		return nil
	})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "WORKER_SOURCE_UPLOAD_INVALID" {
		t.Fatalf("source reuse err=%v", err)
	}
	var reused int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM krun_worker_builds WHERE build_id='source-build-reuse'`).Scan(&reused); err != nil || reused != 0 {
		t.Fatalf("reused build persisted count=%d err=%v", reused, err)
	}

	// Once build metadata is retained no longer, GC may claim and remove it.
	if _, err := store.pool.Exec(ctx, `DELETE FROM krun_worker_build_private WHERE build_id='source-build'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `DELETE FROM krun_worker_builds WHERE build_id='source-build'`); err != nil {
		t.Fatal(err)
	}
	candidates, err = store.garbageWorkerSourceUploads(ctx, now().Add(-22*time.Hour), 10)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("post-retention candidates=%+v err=%v", candidates, err)
	}
	if claimed, err := store.claimGarbageWorkerSourceUpload(ctx, key, "gc-winner", candidates[0].UseRevision); err != nil || !claimed {
		t.Fatalf("unreferenced source was not claimable: claimed=%v err=%v", claimed, err)
	}
	if err := store.deleteGarbageWorkerSourceUpload(ctx, key, "gc-winner"); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresBuildRetentionUsesStableTieBreakAndRemovesPrivateOrphans(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	if err := store.Update(ctx, PartitionIAM, func(st *State) error {
		st.Organizations["retention-org"] = Organization{ID: "retention-org", Name: "Retention", Revision: 1}
		st.Resources["retention-service"] = Resource{ID: "retention-service", Kind: "service", Organization: "retention-org", Name: "Retention", Revision: 1}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	created := now().Add(-48 * time.Hour).UTC().Truncate(time.Second)
	for i := 0; i < workerBuildRetainPerService+1; i++ {
		id := "retention-build-" + leftPad(itoa(i), 3)
		if _, err := store.pool.Exec(ctx, `INSERT INTO krun_worker_builds
			(build_id, org_id, service_id, env_name, state, entrypoint, created_at)
			VALUES ($1,'retention-org','retention-service','production','failed','index.ts',$2)`, id, created); err != nil {
			t.Fatal(err)
		}
		if _, err := store.pool.Exec(ctx, `INSERT INTO krun_worker_build_private
			(build_id, actor_user_id, actor_auth_version, log) VALUES ($1,'actor',1,'log')`, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO krun_worker_build_private
		(build_id, actor_user_id, actor_auth_version, log) VALUES ('retention-orphan','actor',1,'orphan')`); err != nil {
		t.Fatal(err)
	}
	if err := store.retainExpired(ctx); err != nil {
		t.Fatal(err)
	}
	var builds, private, orphans int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM krun_worker_builds WHERE service_id='retention-service'`).Scan(&builds); err != nil {
		t.Fatal(err)
	}
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM krun_worker_build_private WHERE build_id LIKE 'retention-build-%'`).Scan(&private); err != nil {
		t.Fatal(err)
	}
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM krun_worker_build_private WHERE build_id='retention-orphan'`).Scan(&orphans); err != nil {
		t.Fatal(err)
	}
	if builds != workerBuildRetainPerService || private != workerBuildRetainPerService || orphans != 0 {
		t.Fatalf("retention builds=%d private=%d orphan=%d", builds, private, orphans)
	}
}

func leftPad(value string, width int) string {
	for len(value) < width {
		value = "0" + value
	}
	return value
}
