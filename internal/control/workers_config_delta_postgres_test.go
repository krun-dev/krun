package control

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestPostgresConfigDeltaMovesAndDeletesRoutes(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	moving := workerConfigRoute{Service: "moving", Host: "move.workers.test", Pool: "old", Path: "/", Enabled: true, Release: "shared"}
	keeping := workerConfigRoute{Service: "keeping", Host: "keep.workers.test", Pool: "old", Path: "/", Enabled: true, Release: "kept"}
	deleting := workerConfigRoute{Service: "deleting", Host: "delete.workers.test", Pool: "old", Path: "/", Enabled: true, Release: "orphan"}
	before := workerConfigProjection{
		Routes: []workerConfigRoute{deleting, keeping, moving},
		Releases: map[string]WorkerRelease{
			"shared": {ID: "shared"}, "kept": {ID: "kept"}, "orphan": {ID: "orphan"},
		},
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO worker_config_dirty(service_id) VALUES ('moving'), ('keeping'), ('deleting')`); err != nil {
		t.Fatal(err)
	}
	dirty, err := store.dirtyWorkerServices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if revision, won, err := store.appendConfigDelta(ctx, 0, dirty, diffWorkerConfig(nil, before)); err != nil || !won || revision != 1 {
		t.Fatalf("seed revision=%d won=%v err=%v", revision, won, err)
	}
	if _, err := store.pool.Exec(ctx, `UPDATE worker_config_checkpoints SET published_revision=revision`); err != nil {
		t.Fatal(err)
	}
	moving.Pool = "new"
	after := workerConfigProjection{
		Routes:   []workerConfigRoute{keeping, moving},
		Releases: map[string]WorkerRelease{"shared": {ID: "shared"}, "kept": {ID: "kept"}},
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO worker_config_dirty(service_id) VALUES ('moving'), ('deleting')`); err != nil {
		t.Fatal(err)
	}
	dirty, err = store.dirtyWorkerServices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ops := diffWorkerConfig(&before, after)
	if revision, won, err := store.appendConfigDelta(ctx, 1, dirty, ops); err != nil || !won || revision != 2 {
		t.Fatalf("move revision=%d won=%v err=%v", revision, won, err)
	}
	projection, err := store.publishedWorkerServices(ctx, []string{"moving", "keeping", "deleting"})
	if err != nil || !reflect.DeepEqual(projection, after) {
		t.Fatalf("projection=%+v want=%+v err=%v", projection, after, err)
	}
	var releaseLocations []string
	rows, err := store.pool.Query(ctx, `SELECT release_id || '/' || pool FROM worker_config_releases ORDER BY release_id, pool`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var location string
		if err := rows.Scan(&location); err != nil {
			t.Fatal(err)
		}
		releaseLocations = append(releaseLocations, location)
	}
	err = rows.Err()
	rows.Close()
	if err != nil || !reflect.DeepEqual(releaseLocations, []string{"kept/old", "shared/new"}) {
		t.Fatalf("release locations=%v err=%v", releaseLocations, err)
	}
	for _, location := range []struct {
		pool, host string
		published  int64
	}{
		{"old", moving.Host, 1},
		{"new", moving.Host, 0},
		{"old", deleting.Host, 1},
	} {
		var revision, published int64
		shard := bytesDigest([]byte(location.host))[:2]
		err := store.pool.QueryRow(ctx, `SELECT revision, published_revision FROM worker_config_checkpoints WHERE pool=$1 AND shard=$2`, location.pool, shard).Scan(&revision, &published)
		if err != nil || revision != 2 || published != location.published {
			t.Fatalf("checkpoint %s/%s revision=%d published=%d err=%v", location.pool, shard, revision, published, err)
		}
	}
	revisions, _, err := store.ConfigRevisionsSince(ctx, 1, 1)
	if err != nil || len(revisions) != 1 {
		t.Fatalf("feed=%+v err=%v", revisions, err)
	}
	var recorded []workerConfigOp
	if err := json.Unmarshal(revisions[0].Ops, &recorded); err != nil || !reflect.DeepEqual(recorded, ops) {
		t.Fatalf("recorded=%+v want=%+v err=%v", recorded, ops, err)
	}
	if dirty, err := store.dirtyWorkerServices(ctx); err != nil || len(dirty) != 0 {
		t.Fatalf("dirty=%+v err=%v", dirty, err)
	}
}

func TestPostgresConfigDeltaRollsBackFailedBatch(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	if _, err := store.pool.Exec(ctx, `INSERT INTO worker_config_dirty(service_id, generation) VALUES ('svc', 1)`); err != nil {
		t.Fatal(err)
	}
	// The release and route writes precede the invalid log entry in the batch.
	// A payload whose host differs from the log's host violates its CHECK.
	ops := []workerConfigOp{
		{Type: "release_put", Pool: "default", Release: &WorkerRelease{ID: "release"}},
		{Type: "route_upsert", Pool: "default", Host: "route.workers.test", Route: &workerConfigRoute{Host: "wrong.workers.test", Release: "release"}},
	}
	revision, won, err := store.appendConfigDelta(ctx, 0, []workerDirtyService{{ID: "svc", Generation: 1}}, ops)
	if err == nil || !strings.Contains(err.Error(), "apply config operations") || won || revision != 0 {
		t.Fatalf("revision=%d won=%v err=%v", revision, won, err)
	}
	for _, table := range []string{"worker_config_revisions", "worker_config_ops", "worker_config_routes", "worker_config_releases", "worker_config_checkpoints"} {
		var count int
		if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s count=%d err=%v", table, count, err)
		}
	}
	var head int64
	if err := store.pool.QueryRow(ctx, `SELECT value::bigint FROM krun_meta WHERE key='worker_config_head'`).Scan(&head); err != nil || head != 0 {
		t.Fatalf("head=%d err=%v", head, err)
	}
	if dirty, err := store.dirtyWorkerServices(ctx); err != nil || len(dirty) != 1 || dirty[0].Generation != 1 {
		t.Fatalf("dirty=%+v err=%v", dirty, err)
	}
}

func TestPostgresConfigDeltaRollsBackCleanupFailureAndRetries(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	host := "delete.workers.test"
	shard := bytesDigest([]byte(host))[:2]
	queries := []string{
		`UPDATE krun_meta SET value='512' WHERE key='worker_config_head'`,
		`INSERT INTO worker_config_revisions(config_revision, digest) VALUES (1, 'expired'), (512, 'current')`,
		`INSERT INTO worker_config_ops(config_revision, ordinal, pool, op_type, host) VALUES (1, 0, 'old', 'route_delete', 'expired.workers.test')`,
		`INSERT INTO worker_config_dirty(service_id, generation) VALUES ('svc', 1)`,
		`INSERT INTO worker_config_releases(release_id, pool, payload) VALUES ('release', 'old', '{"id":"release"}')`,
		`CREATE FUNCTION fail_config_cleanup() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'cleanup failed'; END $$`,
		`CREATE TRIGGER fail_config_cleanup BEFORE DELETE ON worker_config_revisions FOR EACH ROW EXECUTE FUNCTION fail_config_cleanup()`,
	}
	for _, query := range queries {
		if _, err := store.pool.Exec(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = store.pool.Exec(ctx, `DROP TRIGGER IF EXISTS fail_config_cleanup ON worker_config_revisions`)
		_, _ = store.pool.Exec(ctx, `DROP FUNCTION IF EXISTS fail_config_cleanup()`)
	})
	if _, err := store.pool.Exec(ctx, `INSERT INTO worker_config_routes(host, pool, enabled, release_id, service_id, shard) VALUES ($1, 'old', true, 'release', 'svc', $2)`, host, shard); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO worker_config_checkpoints(pool, shard, revision, published_revision) VALUES ('old', $1, 512, 512)`, shard); err != nil {
		t.Fatal(err)
	}
	ops := []workerConfigOp{{Type: "route_delete", Pool: "old", Host: host}}
	dirty := []workerDirtyService{{ID: "svc", Generation: 1}}
	revision, won, err := store.appendConfigDelta(ctx, 512, dirty, ops)
	if err == nil || !strings.Contains(err.Error(), "prune config revisions") || won || revision != 512 {
		t.Fatalf("revision=%d won=%v err=%v", revision, won, err)
	}
	for table, want := range map[string]int{
		"worker_config_revisions": 2, "worker_config_ops": 1, "worker_config_routes": 1,
		"worker_config_releases": 1, "worker_config_dirty": 1,
	} {
		var count int
		if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&count); err != nil || count != want {
			t.Fatalf("%s count=%d want=%d err=%v", table, count, want, err)
		}
	}
	var head, checkpoint, published int64
	if err := store.pool.QueryRow(ctx, `SELECT value::bigint FROM krun_meta WHERE key='worker_config_head'`).Scan(&head); err != nil || head != 512 {
		t.Fatalf("head=%d err=%v", head, err)
	}
	if err := store.pool.QueryRow(ctx, `SELECT revision, published_revision FROM worker_config_checkpoints WHERE pool='old' AND shard=$1`, shard).Scan(&checkpoint, &published); err != nil || checkpoint != 512 || published != 512 {
		t.Fatalf("checkpoint=%d published=%d err=%v", checkpoint, published, err)
	}
	if _, err := store.pool.Exec(ctx, `DROP TRIGGER fail_config_cleanup ON worker_config_revisions`); err != nil {
		t.Fatal(err)
	}
	if revision, won, err := store.appendConfigDelta(ctx, 512, dirty, ops); err != nil || !won || revision != 513 {
		t.Fatalf("retry revision=%d won=%v err=%v", revision, won, err)
	}
	for table, want := range map[string]int{
		"worker_config_revisions": 2, "worker_config_ops": 1, "worker_config_routes": 0,
		"worker_config_releases": 0, "worker_config_dirty": 0,
	} {
		var count int
		if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&count); err != nil || count != want {
			t.Fatalf("retry %s count=%d want=%d err=%v", table, count, want, err)
		}
	}
	if err := store.pool.QueryRow(ctx, `SELECT revision, published_revision FROM worker_config_checkpoints WHERE pool='old' AND shard=$1`, shard).Scan(&checkpoint, &published); err != nil || checkpoint != 513 || published != 512 {
		t.Fatalf("retry checkpoint=%d published=%d err=%v", checkpoint, published, err)
	}
	var expired int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM worker_config_revisions WHERE config_revision=1`).Scan(&expired); err != nil || expired != 0 {
		t.Fatalf("expired revisions=%d err=%v", expired, err)
	}
}

func TestPostgresConfigDeltaNoOpKeepsNewerDirtyGeneration(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	if _, err := store.pool.Exec(ctx, `INSERT INTO worker_config_dirty(service_id, generation) VALUES ('svc', 2)`); err != nil {
		t.Fatal(err)
	}
	if revision, won, err := store.appendConfigDelta(ctx, 0, []workerDirtyService{{ID: "svc", Generation: 1}}, nil); err != nil || !won || revision != 0 {
		t.Fatalf("revision=%d won=%v err=%v", revision, won, err)
	}
	if dirty, err := store.dirtyWorkerServices(ctx); err != nil || len(dirty) != 1 || dirty[0].Generation != 2 {
		t.Fatalf("newer generation lost: dirty=%+v err=%v", dirty, err)
	}
	if revision, won, err := store.appendConfigDelta(ctx, 0, []workerDirtyService{{ID: "svc", Generation: 2}}, nil); err != nil || !won || revision != 0 {
		t.Fatalf("ack revision=%d won=%v err=%v", revision, won, err)
	}
	if dirty, err := store.dirtyWorkerServices(ctx); err != nil || len(dirty) != 0 {
		t.Fatalf("ack dirty=%+v err=%v", dirty, err)
	}
}
