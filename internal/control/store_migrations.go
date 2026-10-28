package control

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type storeMigration struct {
	version int
	name    string
	apply   func(context.Context, pgx.Tx) error
}

// storeMigrations is append-only. A migration is recorded in the same
// transaction as its DDL, so schema_version can never claim work that did not
// commit. Existing installations without the ledger safely replay the
// idempotent baseline before recording it.
var storeMigrations = []storeMigration{
	{
		version: 1,
		name:    "normalized control-plane tables",
		apply: func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, storeSchema)
			return err
		},
	},
	{
		version: 2,
		name:    "audit runtime metadata and monotonic IAM revisions",
		apply: func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `
CREATE TABLE IF NOT EXISTS krun_audits (
	id text PRIMARY KEY,
	time timestamptz NOT NULL,
	actor text NOT NULL DEFAULT '',
	credential text NOT NULL DEFAULT '',
	key_id text NOT NULL DEFAULT '',
	action text NOT NULL DEFAULT '',
	resource text NOT NULL DEFAULT '',
	organization_id text NOT NULL DEFAULT '',
	allowed boolean NOT NULL DEFAULT false,
	request_id text NOT NULL DEFAULT '',
	revision integer NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS krun_audits_time ON krun_audits (time DESC);

CREATE TABLE IF NOT EXISTS worker_config_revisions (
	config_revision bigserial PRIMARY KEY,
	digest text NOT NULL,
	payload jsonb NOT NULL,
	ops jsonb NOT NULL,
	created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS worker_runtime_acks (
	pod_uid text PRIMARY KEY,
	pool text NOT NULL,
	revision bigint NOT NULL,
	updated_at timestamptz NOT NULL
);

CREATE SEQUENCE IF NOT EXISTS krun_revision_seq;
INSERT INTO krun_meta (key, value) VALUES ('revision','0') ON CONFLICT (key) DO NOTHING;
INSERT INTO krun_meta (key, value) VALUES ('bootstrapped','0') ON CONFLICT (key) DO NOTHING;
SELECT setval(
	'krun_revision_seq',
	GREATEST((SELECT value::bigint FROM krun_meta WHERE key='revision'), 1),
	(SELECT value::bigint > 0 FROM krun_meta WHERE key='revision')
);
`)
			return err
		},
	},
	{
		version: 3,
		name:    "distributed authentication rate limits",
		apply: func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `
CREATE TABLE IF NOT EXISTS krun_rate_limits (
	scope text NOT NULL,
	key_hash text NOT NULL,
	window_start timestamptz NOT NULL,
	count integer NOT NULL CHECK (count > 0),
	PRIMARY KEY (scope, key_hash, window_start)
);
CREATE INDEX IF NOT EXISTS krun_rate_limits_window ON krun_rate_limits (window_start);
`)
			return err
		},
	},
	{
		version: 4,
		name:    "organization revisions",
		apply: func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `ALTER TABLE krun_organizations ADD COLUMN IF NOT EXISTS revision integer NOT NULL DEFAULT 1`)
			return err
		},
	},
	{
		version: 5,
		name:    "artifact garbage collection leases",
		apply: func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `
ALTER TABLE krun_worker_artifacts ADD COLUMN IF NOT EXISTS gc_claimed_by text NOT NULL DEFAULT '';
ALTER TABLE krun_worker_artifacts ADD COLUMN IF NOT EXISTS gc_claimed_at timestamptz;
`)
			return err
		},
	},
	{
		version: 6,
		name:    "unique CLI device user codes",
		apply: func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `
DELETE FROM krun_device_codes a USING krun_device_codes b
WHERE a.user_code=b.user_code AND (
	a.expires_at < b.expires_at OR (a.expires_at=b.expires_at AND a.code_hash < b.code_hash)
);
DROP INDEX IF EXISTS krun_device_codes_user_code;
CREATE UNIQUE INDEX IF NOT EXISTS krun_device_codes_user_code_unique ON krun_device_codes (user_code);
`)
			return err
		},
	},
	{
		version: 7,
		name:    "repository connect organization context",
		apply: func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `ALTER TABLE krun_repository_connect_states ADD COLUMN IF NOT EXISTS org_id text NOT NULL DEFAULT ''`)
			return err
		},
	},
	{
		version: 8,
		name:    "atomic email send guards",
		apply: func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `
CREATE TABLE IF NOT EXISTS krun_email_send_locks (
	scope text NOT NULL,
	key_hash text NOT NULL,
	touched_at timestamptz NOT NULL,
	PRIMARY KEY (scope, key_hash)
);
CREATE INDEX IF NOT EXISTS krun_email_send_locks_age ON krun_email_send_locks (touched_at);
`)
			return err
		},
	},
	{
		version: 9,
		name:    "artifact usage revisions",
		apply: func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `ALTER TABLE krun_worker_artifacts ADD COLUMN IF NOT EXISTS use_revision bigint NOT NULL DEFAULT 0`)
			return err
		},
	},
	{
		version: 10,
		name:    "worker service domain and preview revisions",
		apply: func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `
ALTER TABLE krun_worker_services ADD COLUMN IF NOT EXISTS domains_revision integer NOT NULL DEFAULT 0;
ALTER TABLE krun_worker_services ADD COLUMN IF NOT EXISTS preview_revision integer NOT NULL DEFAULT 0;
`)
			return err
		},
	},
	{
		version: 11,
		name:    "normalized worker config head",
		apply: func(ctx context.Context, tx pgx.Tx) error {
			return migrateNormalizedWorkerConfig(ctx, tx)
		},
	},
	{
		version: 12,
		name:    "worker source upload reservations",
		apply: func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `
CREATE TABLE IF NOT EXISTS krun_worker_source_uploads (
	object_key text PRIMARY KEY,
	org_id text NOT NULL,
	state text NOT NULL,
	build_id text NOT NULL DEFAULT '',
	use_revision bigint NOT NULL DEFAULT 0,
	created_at timestamptz NOT NULL DEFAULT now(),
	gc_claimed_by text NOT NULL DEFAULT '',
	gc_claimed_at timestamptz
);
CREATE INDEX IF NOT EXISTS krun_worker_source_uploads_created ON krun_worker_source_uploads (created_at);
INSERT INTO krun_worker_source_uploads
	(object_key, org_id, state, build_id, use_revision, created_at)
SELECT private.source_key, build.org_id, 'reserved', private.build_id, 1, build.created_at
FROM krun_worker_build_private private
JOIN krun_worker_builds build ON build.build_id=private.build_id
WHERE private.source_key <> ''
ON CONFLICT (object_key) DO NOTHING;
`)
			return err
		},
	},
	{
		version: 13,
		name:    "atomic active build capacity",
		apply:   migrateWorkerActiveBuildCount,
	},
	{
		version: 14,
		name:    "repository OAuth token lifecycle",
		apply: func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `
ALTER TABLE krun_repository_connections ADD COLUMN IF NOT EXISTS token_type text NOT NULL DEFAULT '';
ALTER TABLE krun_repository_connections ADD COLUMN IF NOT EXISTS refresh_token text NOT NULL DEFAULT '';
ALTER TABLE krun_repository_connections ADD COLUMN IF NOT EXISTS expires_at timestamptz;
ALTER TABLE krun_repository_connections ADD COLUMN IF NOT EXISTS token_revision bigint NOT NULL DEFAULT 1;
ALTER TABLE krun_repository_connections ADD COLUMN IF NOT EXISTS refresh_state text NOT NULL DEFAULT 'ready';
ALTER TABLE krun_repository_connections ADD COLUMN IF NOT EXISTS refresh_claimed_at timestamptz;
ALTER TABLE krun_repository_connections ADD COLUMN IF NOT EXISTS reconnect_required boolean NOT NULL DEFAULT false;
-- Pre-v14 rows have no trustworthy expiry or refresh metadata. Fail closed
-- once and ask the user to reconnect rather than treating an expired grant as
-- a permanent connection.
UPDATE krun_repository_connections SET reconnect_required=true
WHERE refresh_token='' AND expires_at IS NULL AND token_revision=1;
DO $$
BEGIN
	IF NOT EXISTS (SELECT 1 FROM pg_constraint
		WHERE conname='krun_repository_connections_refresh_state'
		  AND conrelid='krun_repository_connections'::regclass) THEN
		ALTER TABLE krun_repository_connections
			ADD CONSTRAINT krun_repository_connections_refresh_state
			CHECK (refresh_state IN ('ready','refreshing'));
	END IF;
END $$;
`)
			return err
		},
	},
	{
		version: 15,
		name:    "versioned encrypted worker secrets",
		apply: func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `
CREATE TABLE IF NOT EXISTS krun_worker_secret_versions (
	secret_version_id text PRIMARY KEY,
	org_id text NOT NULL,
	service_id text NOT NULL,
	env_name text NOT NULL,
	name text NOT NULL,
	ciphertext bytea NOT NULL,
	nonce bytea NOT NULL,
	key_version text NOT NULL,
	value_digest text NOT NULL,
	created_by text NOT NULL,
	created_at timestamptz NOT NULL DEFAULT now(),
	CHECK (octet_length(ciphertext) > 16),
	CHECK (octet_length(nonce) = 12)
);
CREATE INDEX IF NOT EXISTS krun_worker_secret_versions_scope
	ON krun_worker_secret_versions (service_id, env_name, name, created_at DESC);
CREATE TABLE IF NOT EXISTS krun_worker_environment_secrets (
	service_id text NOT NULL,
	env_name text NOT NULL,
	name text NOT NULL,
	secret_version_id text NOT NULL,
	PRIMARY KEY (service_id, env_name, name)
);
ALTER TABLE krun_worker_releases ADD COLUMN IF NOT EXISTS secret_digest text NOT NULL DEFAULT '';
CREATE TABLE IF NOT EXISTS krun_worker_release_secrets (
	release_id text NOT NULL,
	name text NOT NULL,
	secret_version_id text NOT NULL,
	PRIMARY KEY (release_id, name)
);
`)
			return err
		},
	},
	{
		version: 16,
		name:    "hidden worker runtime pool assignment",
		apply: func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `
ALTER TABLE krun_worker_services ADD COLUMN IF NOT EXISTS pool text NOT NULL DEFAULT 'default';
ALTER TABLE worker_config_routes ADD COLUMN IF NOT EXISTS pool text NOT NULL DEFAULT 'default';
ALTER TABLE worker_config_releases ADD COLUMN IF NOT EXISTS pool text NOT NULL DEFAULT 'default';
ALTER TABLE worker_config_ops ADD COLUMN IF NOT EXISTS pool text NOT NULL DEFAULT 'default';
UPDATE worker_config_routes SET pool=COALESCE(NULLIF(pool,''),'default');
UPDATE worker_config_releases SET pool=COALESCE(NULLIF(payload->>'pool',''),pool,'default');
UPDATE worker_config_ops SET pool=COALESCE(NULLIF(payload->>'pool',''),pool,'default');
			`)
			return err
		},
	},
}

func migrateNormalizedWorkerConfig(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `
CREATE TABLE IF NOT EXISTS worker_config_routes (
	host text PRIMARY KEY,
	pool text NOT NULL DEFAULT 'default',
	path text NOT NULL DEFAULT '',
	enabled boolean NOT NULL,
	release_id text NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS worker_config_releases (
	release_id text PRIMARY KEY,
	pool text NOT NULL DEFAULT 'default',
	payload jsonb NOT NULL CHECK (jsonb_typeof(payload) = 'object')
);

CREATE TABLE IF NOT EXISTS worker_config_ops (
	config_revision bigint NOT NULL,
	ordinal integer NOT NULL CHECK (ordinal >= 0),
	pool text NOT NULL DEFAULT 'default',
	op_type text NOT NULL CHECK (op_type IN ('release_put','route_upsert','route_delete')),
	host text NOT NULL DEFAULT '',
	release_id text NOT NULL DEFAULT '',
	payload jsonb CHECK (payload IS NULL OR jsonb_typeof(payload) = 'object'),
	PRIMARY KEY (config_revision, ordinal),
	CHECK (
		(op_type='release_put' AND host='' AND release_id<>'' AND payload IS NOT NULL AND payload->>'id'=release_id)
		OR (op_type='route_upsert' AND host<>'' AND payload IS NOT NULL AND payload->>'host'=host AND COALESCE(payload->>'release','')=release_id)
		OR (op_type='route_delete' AND host<>'' AND release_id='' AND payload IS NULL)
	)
);
`); err != nil {
		return err
	}
	var hasPayload, hasOps bool
	if err := tx.QueryRow(ctx, `SELECT
		EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='worker_config_revisions' AND column_name='payload'),
		EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='worker_config_revisions' AND column_name='ops')`).Scan(&hasPayload, &hasOps); err != nil {
		return err
	}
	if hasPayload {
		if _, err := tx.Exec(ctx, `DELETE FROM worker_config_routes;
			DELETE FROM worker_config_releases;
			WITH latest AS (
				SELECT payload FROM worker_config_revisions ORDER BY config_revision DESC LIMIT 1
			)
			INSERT INTO worker_config_routes (host, path, enabled, release_id)
			SELECT route->>'host', COALESCE(route->>'path',''), COALESCE((route->>'enabled')::boolean,false), COALESCE(route->>'release','')
			FROM latest, LATERAL jsonb_array_elements(COALESCE(latest.payload->'routes','[]'::jsonb)) AS route
			ON CONFLICT (host) DO UPDATE SET path=excluded.path, enabled=excluded.enabled, release_id=excluded.release_id;

			WITH latest AS (
				SELECT payload FROM worker_config_revisions ORDER BY config_revision DESC LIMIT 1
			)
			INSERT INTO worker_config_releases (release_id, payload)
			SELECT item.key, item.value
			FROM latest, LATERAL jsonb_each(COALESCE(latest.payload->'releases','{}'::jsonb)) AS item
			ON CONFLICT (release_id) DO UPDATE SET payload=excluded.payload`); err != nil {
			return err
		}
	}
	if hasOps {
		if _, err := tx.Exec(ctx, `DELETE FROM worker_config_ops;
			INSERT INTO worker_config_ops (config_revision, ordinal, op_type, host, release_id, payload)
			SELECT revision.config_revision, (item.ordinality-1)::integer,
				item.value->>'type',
				COALESCE(item.value->>'host', item.value#>>'{route,host}', ''),
				COALESCE(item.value#>>'{release,id}', item.value#>>'{route,release}', ''),
				CASE item.value->>'type'
					WHEN 'release_put' THEN item.value->'release'
					WHEN 'route_upsert' THEN item.value->'route'
					WHEN 'route_delete' THEN NULL
				END
			FROM worker_config_revisions revision,
				LATERAL jsonb_array_elements(revision.ops) WITH ORDINALITY AS item(value, ordinality)
			ON CONFLICT (config_revision, ordinal) DO UPDATE SET
				op_type=excluded.op_type, host=excluded.host,
				release_id=excluded.release_id, payload=excluded.payload`); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO krun_meta (key, value)
		SELECT 'worker_config_head', COALESCE(MAX(config_revision),0)::text FROM worker_config_revisions
		ON CONFLICT (key) DO UPDATE SET value=GREATEST(krun_meta.value::bigint, excluded.value::bigint)::text;
		INSERT INTO krun_meta (key, value) VALUES ('worker_topology_generation','0') ON CONFLICT (key) DO NOTHING;
		ALTER TABLE worker_config_revisions DROP COLUMN IF EXISTS payload;
		ALTER TABLE worker_config_revisions DROP COLUMN IF EXISTS ops`); err != nil {
		return err
	}
	return nil
}

func applyStoreMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	if len(storeMigrations) == 0 || itoa(storeMigrations[len(storeMigrations)-1].version) != storeSchemaVersion {
		return fmt.Errorf("storeSchemaVersion does not match migration ledger")
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS krun_schema_migrations (
		version integer PRIMARY KEY,
		name text NOT NULL,
		applied_at timestamptz NOT NULL DEFAULT now()
	)`); err != nil {
		return err
	}
	var maximum, count int
	if err := pool.QueryRow(ctx, `SELECT COALESCE(max(version),0), count(*) FROM krun_schema_migrations`).Scan(&maximum, &count); err != nil {
		return err
	}
	latest := storeMigrations[len(storeMigrations)-1].version
	if maximum > latest {
		return fmt.Errorf("database schema version %d is newer than supported version %d", maximum, latest)
	}
	if count != maximum {
		return fmt.Errorf("store migration ledger is not a contiguous sequence")
	}
	for _, migration := range storeMigrations {
		if err := applyStoreMigration(ctx, pool, migration); err != nil {
			return err
		}
	}
	return nil
}

func applyStoreMigration(ctx context.Context, pool *pgxpool.Pool, migration storeMigration) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	tag, err := tx.Exec(ctx, `INSERT INTO krun_schema_migrations (version, name) VALUES ($1,$2) ON CONFLICT (version) DO NOTHING`, migration.version, migration.name)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		var name string
		if err = tx.QueryRow(ctx, `SELECT name FROM krun_schema_migrations WHERE version=$1`, migration.version).Scan(&name); err != nil {
			return err
		}
		if name != migration.name {
			return fmt.Errorf("store migration %d name mismatch", migration.version)
		}
		return tx.Commit(ctx)
	}
	if err = migration.apply(ctx, tx); err != nil {
		return fmt.Errorf("apply store migration %d (%s): %w", migration.version, migration.name, err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO krun_meta (key, value) VALUES ('schema_version',$1) ON CONFLICT (key) DO UPDATE SET value=excluded.value`, itoa(migration.version)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
