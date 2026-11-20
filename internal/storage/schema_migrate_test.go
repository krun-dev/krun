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
        DROP TABLE krun_schema_migrations; DROP TABLE krun_worker_address_claims; DROP TABLE krun_worker_cleanup;
		INSERT INTO krun_meta(key,value) VALUES ('schema_baseline','82934894c84ba5a9ebef6ff99f2038684c8649d07d0bc79c30b27ea939d0f9a1');
		DROP TABLE krun_worker_hostname_routes, krun_worker_hostnames;
		ALTER TABLE krun_worker_versions DROP COLUMN version_url CASCADE;
		ALTER TABLE worker_config_routes DROP CONSTRAINT worker_config_routes_pkey,
			DROP COLUMN route_key, DROP COLUMN match_type, ADD PRIMARY KEY(host);
		ALTER TABLE worker_config_ops DROP COLUMN route_key;
		INSERT INTO worker_config_routes(host,pool,worker_id,shard,enabled,path)
			VALUES('w-old-production.workers.example.com','default','old-worker','aa',true,'/');
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
	var routeKey, path string
	if err := pool.QueryRow(ctx, `SELECT route_key,path FROM worker_config_routes
		WHERE host='w-old-production.workers.example.com'`).Scan(&routeKey, &path); err != nil || routeKey != "" || path != "/" {
		t.Fatalf("system hostname changed: key=%q path=%q err=%v", routeKey, path, err)
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

func TestPostgresSchemaUpgradePreservesManagedWorkerRoutes(t *testing.T) {
	schema := testpgx.NewSchema(t, "krun_hostname_upgrade")
	ctx := t.Context()
	store, err := Open(ctx, schema.DSN, RepositoryTokenKeys{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err = store.pool.Exec(ctx, `
        DROP TABLE krun_schema_migrations; DROP TABLE krun_worker_address_claims; DROP TABLE krun_worker_cleanup;
		INSERT INTO krun_meta(key,value) VALUES ('schema_baseline','82934894c84ba5a9ebef6ff99f2038684c8649d07d0bc79c30b27ea939d0f9a1');
		ALTER TABLE krun_workers DROP COLUMN workers_dev_disabled, DROP COLUMN preview_urls_disabled;
		ALTER TABLE krun_worker_hostnames DROP COLUMN gateway_managed, DROP COLUMN domain_type;
		ALTER TABLE krun_worker_versions DROP COLUMN version_url CASCADE;
		ALTER TABLE krun_worker_hostname_routes DROP COLUMN origin, DROP COLUMN cf_pattern, DROP COLUMN custom_domain CASCADE, DROP COLUMN previews_enabled;
		INSERT INTO krun_workers(worker_id,org_id) VALUES ('old-worker','old-org');
		INSERT INTO krun_worker_hostnames(host,org_id,suffix_id,revision)
			VALUES ('shop.workers.example.com','old-org','public',5);
		INSERT INTO krun_worker_hostname_routes(host,match_type,path,worker_id,enabled)
			VALUES ('shop.workers.example.com','prefix','/api','old-worker',true);
		UPDATE krun_meta SET value='23c469b3647eb09d4a9e9355d92f4d9e70f230381b56c25408fc141c1927e5c3' WHERE key='schema_baseline';
	`); err != nil {
		t.Fatal(err)
	}
	if applied, err := MigrateSchema(ctx, schema.DSN); !applied || err != nil {
		t.Fatalf("upgrade from Worker-only hostnames: applied=%t err=%v", applied, err)
	}
	host, err := store.ManagedHostname(ctx, "old-org", "shop.workers.example.com")
	if err != nil || host.Revision != 5 || host.Type != "application" || host.GatewayManaged || len(host.Routes) != 1 {
		t.Fatalf("legacy hostname was changed: %+v (%v)", host, err)
	}
	rule := host.Routes[0]
	if rule.Worker != "old-worker" || rule.Path != "/api" || !rule.Enabled || rule.CustomDomain || rule.Origin != "" {
		t.Fatalf("legacy route was changed: %+v", rule)
	}
	var workersDevDisabled, previewURLsDisabled bool
	err = store.pool.QueryRow(ctx, `SELECT workers_dev_disabled,preview_urls_disabled FROM krun_workers
		WHERE worker_id='old-worker'`).Scan(&workersDevDisabled, &previewURLsDisabled)
	if err != nil || workersDevDisabled || previewURLsDisabled {
		t.Fatalf("default addresses changed: disabled=%t/%t (%v)", workersDevDisabled, previewURLsDisabled, err)
	}
}

func TestPostgresSchemaUpgradeRecordsManagedHostnameTypes(t *testing.T) {
	schema := testpgx.NewSchema(t, "krun_hostname_types_upgrade")
	ctx := t.Context()
	store, err := Open(ctx, schema.DSN, RepositoryTokenKeys{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err = store.pool.Exec(ctx, `
        DROP TABLE krun_schema_migrations; DROP TABLE krun_worker_address_claims; DROP TABLE krun_worker_cleanup;
		INSERT INTO krun_meta(key,value) VALUES ('schema_baseline','82934894c84ba5a9ebef6ff99f2038684c8649d07d0bc79c30b27ea939d0f9a1');
		ALTER TABLE krun_worker_hostnames DROP COLUMN domain_type;
		ALTER TABLE krun_worker_hostname_routes DROP COLUMN previews_enabled;
		ALTER TABLE krun_worker_versions DROP COLUMN version_url CASCADE;
		INSERT INTO krun_worker_hostnames(host,org_id,suffix_id,gateway_managed)
			VALUES ('custom.workers.example.com','old-org','public',false),
			       ('mixed.workers.example.com','old-org','public',true),
			       ('empty.workers.example.com','old-org','public',false);
		INSERT INTO krun_worker_hostname_routes(host,match_type,path,worker_id,origin,custom_domain,enabled)
			VALUES ('custom.workers.example.com','prefix','/','old-worker','',true,true),
			       ('mixed.workers.example.com','prefix','/','old-worker','',true,true),
			       ('mixed.workers.example.com','prefix','/static','','site.example.com',false,true);
		UPDATE krun_meta SET value='5c08400c3a40273803d6423338e25adaade59c4db129072a0761113c9644b423' WHERE key='schema_baseline';
	`); err != nil {
		t.Fatal(err)
	}
	if applied, err := MigrateSchema(ctx, schema.DSN); !applied || err != nil {
		t.Fatalf("upgrade from shared hostnames: applied=%t err=%v", applied, err)
	}
	var dirty bool
	if err := store.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM worker_config_dirty WHERE worker_id='old-worker')`).Scan(&dirty); err != nil || !dirty {
		t.Fatalf("upgraded custom Preview was not queued for publication: dirty=%t err=%v", dirty, err)
	}
	for _, tc := range []struct {
		label, kind string
		routes      int
		custom      bool
	}{
		{"custom", "worker_custom", 1, true},
		{"mixed", "application", 2, false},
		{"empty", "application", 0, false},
	} {
		item, err := store.ManagedHostname(ctx, "old-org", tc.label+".workers.example.com")
		if err != nil || item.Type != tc.kind || len(item.Routes) != tc.routes {
			t.Fatalf("%s: unexpected hostname %+v (%v)", tc.label, item, err)
		}
		for _, route := range item.Routes {
			if route.CustomDomain != tc.custom || route.PreviewsEnabled != tc.custom {
				t.Fatalf("%s: unexpected route %+v", tc.label, route)
			}
		}
	}
}
