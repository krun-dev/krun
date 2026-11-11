package storage

import (
	"context"
	"net/url"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/krun-dev/krun/internal/testpgx"
)

// The fingerprint of the last schema before local-project upload (e.g. 96b6dd4).
const uploadPredecessor = "8f802b41ccf74f398f6c4c8ede9c480ad29b7e29ae95d7febda6046baa3842ef"
const claimPredecessor = "791bb9ebf10e4652f69918635175574492833a5b55b53db9806f0a604b333a30"

func preUploadDatabase(t *testing.T) (string, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	schema := testpgx.NewSchema(t, "krun_upgrade")
	store, err := Open(ctx, schema.DSN, RepositoryTokenKeys{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)

	if _, err := store.pool.Exec(ctx, `
		ALTER TABLE krun_worker_build_private DROP COLUMN config_path;
		ALTER TABLE krun_worker_operations DROP COLUMN claim_generation;
		ALTER TABLE krun_worker_operations DROP COLUMN edge_pool;
		ALTER TABLE worker_runtime_acks DROP COLUMN config_epoch, ADD PRIMARY KEY(pod_uid);
		DELETE FROM krun_meta WHERE key='worker_config_epoch';
		INSERT INTO krun_worker_build_private(build_id,actor_user_id,actor_auth_version,source_key)
			VALUES('old-build','old-user',1,'retained-source');
		INSERT INTO krun_worker_operations(operation_id,kind,org_id,worker_id,env_name,state,claimed_by,claimed_at)
			VALUES('old-operation','publish','old-org','old-worker','production','validating','old-replica',now());
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `UPDATE krun_meta SET value=$1 WHERE key='schema_baseline'`, uploadPredecessor); err != nil {
		t.Fatal(err)
	}
	return schema.DSN, store.pool
}

func TestPostgresSchemaUpgradePreservesBuildAndCanRetry(t *testing.T) {
	dsn, pool := preUploadDatabase(t)
	ctx := context.Background()
	if store, err := Open(ctx, dsn, RepositoryTokenKeys{}); err == nil {
		store.Close()
		t.Fatal("normal startup accepted the old fingerprint")
	}

	// Two operators using the same release should yield exactly one migration.
	type result struct {
		applied bool
		err     error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	for range 2 {
		go func() {
			<-start
			applied, err := MigrateSchema(ctx, dsn)
			results <- result{applied, err}
		}()
	}
	close(start)
	applied := 0
	for range 2 {
		res := <-results
		if res.err != nil {
			t.Fatal(res.err)
		}
		if res.applied {
			applied++
		}
	}
	if applied != 1 {
		t.Fatalf("expected one migration, got %d", applied)
	}

	store, err := Open(ctx, dsn, RepositoryTokenKeys{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var actor, source, configPath string
	if err := pool.QueryRow(ctx, `SELECT actor_user_id,source_key,config_path
		FROM krun_worker_build_private WHERE build_id='old-build'`).Scan(&actor, &source, &configPath); err != nil {
		t.Fatal(err)
	}
	if actor != "old-user" || source != "retained-source" || configPath != "" {
		t.Fatalf("unexpected migrated build: %q %q %q", actor, source, configPath)
	}
	if _, owned, err := store.ClaimOperation(ctx, "old-operation", "new-replica"); err != nil || !owned {
		t.Fatalf("resume a validating operation from the old schema: owned=%t err=%v", owned, err)
	}
}

func TestPostgresSchemaUpgradeRollsBackDDLOnFingerprintFailure(t *testing.T) {
	dsn, pool := preUploadDatabase(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		CREATE FUNCTION refuse_baseline_update() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			RAISE EXCEPTION 'test rejects fingerprint change';
		END;
		$$;
		CREATE TRIGGER refuse_baseline_update BEFORE UPDATE ON krun_meta
			FOR EACH ROW WHEN (OLD.key='schema_baseline')
			EXECUTE FUNCTION refuse_baseline_update();
	`); err != nil {
		t.Fatal(err)
	}
	if applied, err := MigrateSchema(ctx, dsn); applied || err == nil {
		t.Fatalf("failed upgrade: applied=%t err=%v", applied, err)
	}

	var baseline, source string
	var configPathExists bool
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT value FROM krun_meta WHERE key='schema_baseline'),
			(SELECT source_key FROM krun_worker_build_private WHERE build_id='old-build'),
			EXISTS (SELECT FROM information_schema.columns WHERE table_schema=current_schema() AND
				((table_name='krun_worker_build_private' AND column_name='config_path') OR
				 (table_name='krun_worker_operations' AND column_name IN ('claim_generation','edge_pool'))))
	`).Scan(&baseline, &source, &configPathExists); err != nil {
		t.Fatal(err)
	}
	if baseline != uploadPredecessor || source != "retained-source" || configPathExists {
		t.Fatalf("failed transaction changed database: baseline=%q source=%q column=%t", baseline, source, configPathExists)
	}

	if _, err := pool.Exec(ctx, `DROP TRIGGER refuse_baseline_update ON krun_meta`); err != nil {
		t.Fatal(err)
	}
	if applied, err := MigrateSchema(ctx, dsn); !applied || err != nil {
		t.Fatalf("retry after failure: applied=%t err=%v", applied, err)
	}
}

func TestPostgresSchemaUpgradeFromClaimPredecessor(t *testing.T) {
	dsn, pool := preUploadDatabase(t)
	ctx := t.Context()
	if _, err := pool.Exec(ctx, `
		ALTER TABLE krun_worker_build_private ADD COLUMN config_path text NOT NULL DEFAULT '';
		UPDATE krun_worker_build_private SET config_path='src/wrangler.toml' WHERE build_id='old-build';
		INSERT INTO krun_workers(worker_id,org_id) VALUES('old-worker','old-org');
		INSERT INTO krun_worker_environments(worker_id,env_name,enabled,version_id,deployment_revision)
			VALUES('old-worker','production',true,'rel-existing',4);
		INSERT INTO krun_worker_operations(operation_id,kind,org_id,worker_id,env_name,state,version_id,deployment_revision,edge_revision)
			VALUES ('op-current','publish','old-org','old-worker','production','committed','rel-existing',4,20),
			       ('op-orphan','publish','old-org','old-worker','deleted-preview','committed','rel-orphan',2,19);
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE krun_meta SET value=$1 WHERE key='schema_baseline'`, claimPredecessor); err != nil {
		t.Fatal(err)
	}
	if applied, err := MigrateSchema(ctx, dsn); !applied || err != nil {
		t.Fatalf("upgrade from claim predecessor: applied=%t err=%v", applied, err)
	}
	var configPath string
	if err := pool.QueryRow(ctx, `SELECT config_path FROM krun_worker_build_private
		WHERE build_id='old-build'`).Scan(&configPath); err != nil || configPath != "src/wrangler.toml" {
		t.Fatalf("existing config path changed: path=%q err=%v", configPath, err)
	}
	var edgeRevision int64
	var orphanState string
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT edge_revision FROM krun_worker_operations WHERE operation_id='op-current'),
		(SELECT state FROM krun_worker_operations WHERE operation_id='op-orphan')`).Scan(&edgeRevision, &orphanState); err != nil {
		t.Fatal(err)
	}
	if edgeRevision != 0 || orphanState != "superseded" {
		t.Fatalf("legacy operations were not reconciled: edge=%d orphan state=%q", edgeRevision, orphanState)
	}
}

func TestPostgresSchemaUpgradeRejectsUnknownAndEmptyDatabases(t *testing.T) {
	ctx := context.Background()
	dsn, pool := preUploadDatabase(t)
	empty := testpgx.NewSchema(t, "krun_upgrade_empty")
	search, err := url.Parse(empty.DSN)
	if err != nil {
		t.Fatal(err)
	}
	query := search.Query()
	query.Set("search_path", empty.Name+","+pool.Config().ConnConfig.RuntimeParams["search_path"])
	search.RawQuery = query.Encode()
	if applied, err := MigrateSchema(ctx, search.String()); applied || err == nil {
		t.Fatalf("empty schema with an upgradable fallback: applied=%t err=%v", applied, err)
	}
	var count int
	if err := empty.Admin.QueryRow(ctx, `SELECT count(*) FROM pg_tables WHERE schemaname=$1`, empty.Name).Scan(&count); err != nil || count != 0 {
		t.Fatalf("empty schema changed: count=%d err=%v", count, err)
	}

	var baseline string
	if err := pool.QueryRow(ctx, `SELECT value FROM krun_meta WHERE key='schema_baseline'`).Scan(&baseline); err != nil || baseline != uploadPredecessor {
		t.Fatalf("fallback schema changed: baseline=%q err=%v", baseline, err)
	}

	if _, err := pool.Exec(ctx, `UPDATE krun_meta SET value='unknown' WHERE key='schema_baseline'`); err != nil {
		t.Fatal(err)
	}
	if applied, err := MigrateSchema(ctx, dsn); applied || err == nil {
		t.Fatalf("unknown fingerprint: applied=%t err=%v", applied, err)
	}
	if err := pool.QueryRow(ctx, `SELECT value FROM krun_meta WHERE key='schema_baseline'`).Scan(&baseline); err != nil || baseline != "unknown" {
		t.Fatalf("unknown baseline changed: baseline=%q err=%v", baseline, err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM krun_meta WHERE key='schema_baseline'`); err != nil {
		t.Fatal(err)
	}
	if applied, err := MigrateSchema(ctx, dsn); applied || err == nil {
		t.Fatalf("missing fingerprint: applied=%t err=%v", applied, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM krun_meta WHERE key='schema_baseline'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("missing fingerprint was created: count=%d err=%v", count, err)
	}
}
