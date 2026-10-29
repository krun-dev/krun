package control

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// storeSchema is the only supported control-plane schema. The project has not
// shipped yet, so startup creates this baseline directly; there is deliberately
// no migration ledger or compatibility import path.
//
// No foreign keys: reference integrity comes from write-path validation,
// explicit cascade deletes in the same statement batch, and reconciliation
// queries for orphans. See AGENTS.md section 9.
const storeSchema = `
CREATE TABLE IF NOT EXISTS krun_meta (
	key   text PRIMARY KEY,
	value text NOT NULL
);

CREATE SEQUENCE IF NOT EXISTS krun_revision_seq;
INSERT INTO krun_meta (key, value) VALUES
	('revision','0'),
	('bootstrapped','0'),
	('worker_config_head','0'),
	('worker_topology_generation','0')
ON CONFLICT (key) DO NOTHING;
SELECT setval(
	'krun_revision_seq',
	GREATEST((SELECT value::bigint FROM krun_meta WHERE key='revision'), 1),
	(SELECT value::bigint > 0 FROM krun_meta WHERE key='revision')
);

CREATE TABLE IF NOT EXISTS krun_audits (
	id              text PRIMARY KEY,
	time            timestamptz NOT NULL,
	actor           text NOT NULL DEFAULT '',
	credential      text NOT NULL DEFAULT '',
	key_id          text NOT NULL DEFAULT '',
	action          text NOT NULL DEFAULT '',
	resource        text NOT NULL DEFAULT '',
	organization_id text NOT NULL DEFAULT '',
	allowed         boolean NOT NULL DEFAULT false,
	request_id      text NOT NULL DEFAULT '',
	revision        integer NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS krun_audits_time ON krun_audits (time DESC);

CREATE TABLE IF NOT EXISTS krun_identity_sources (
	source      text PRIMARY KEY,
	fingerprint text NOT NULL
);

CREATE TABLE IF NOT EXISTS krun_identity_providers (
	provider_id text PRIMARY KEY,
	source      text NOT NULL,
	namespace   text NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS krun_identity_providers_ns ON krun_identity_providers (namespace);

CREATE TABLE IF NOT EXISTS krun_users (
	user_id      text PRIMARY KEY,
	source       text NOT NULL,
	subject      text NOT NULL,
	name         text NOT NULL DEFAULT '',
	email        text NOT NULL DEFAULT '',
	status       text NOT NULL DEFAULT 'active',
	auth_version integer NOT NULL DEFAULT 1,
	created_at   timestamptz NOT NULL DEFAULT now(),
	updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS krun_users_identity ON krun_users (source, subject);

CREATE TABLE IF NOT EXISTS krun_organizations (
	org_id     text PRIMARY KEY,
	name       text NOT NULL,
	revision   integer NOT NULL DEFAULT 1,
	created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS krun_org_sources (
	org_id text NOT NULL,
	source text NOT NULL,
	PRIMARY KEY (org_id, source)
);

CREATE TABLE IF NOT EXISTS krun_members (
	org_id     text NOT NULL,
	user_id    text NOT NULL,
	created_at timestamptz NOT NULL DEFAULT now(),
	PRIMARY KEY (org_id, user_id)
);
CREATE INDEX IF NOT EXISTS krun_members_user ON krun_members (user_id);

CREATE TABLE IF NOT EXISTS krun_groups (
	group_id   text PRIMARY KEY,
	scope_kind text NOT NULL,
	scope_id   text NOT NULL,
	name       text NOT NULL,
	revision   integer NOT NULL DEFAULT 1
);
CREATE INDEX IF NOT EXISTS krun_groups_scope ON krun_groups (scope_kind, scope_id);

CREATE TABLE IF NOT EXISTS krun_group_members (
	group_id text NOT NULL,
	user_id  text NOT NULL,
	PRIMARY KEY (group_id, user_id)
);
CREATE INDEX IF NOT EXISTS krun_group_members_user ON krun_group_members (user_id);

CREATE TABLE IF NOT EXISTS krun_assignments (
	assignment_id text PRIMARY KEY,
	subject_kind  text NOT NULL,
	subject_id    text NOT NULL,
	role          text NOT NULL,
	scope_kind    text NOT NULL,
	scope_id      text NOT NULL,
	conditions    jsonb NOT NULL DEFAULT '{}'::jsonb,
	revision      integer NOT NULL DEFAULT 1
);
CREATE INDEX IF NOT EXISTS krun_assignments_subject ON krun_assignments (subject_kind, subject_id);

CREATE TABLE IF NOT EXISTS krun_resources (
	resource_id text PRIMARY KEY,
	kind        text NOT NULL,
	org_id      text NOT NULL,
	name        text NOT NULL,
	revision    integer NOT NULL DEFAULT 1
);
CREATE INDEX IF NOT EXISTS krun_resources_org ON krun_resources (org_id, kind);

CREATE TABLE IF NOT EXISTS krun_api_keys (
	key_id        text PRIMARY KEY,
	owner_user_id text NOT NULL,
	org_id        text NOT NULL DEFAULT '',
	scope_kind    text NOT NULL,
	name          text NOT NULL,
	prefix        text NOT NULL,
	policy        jsonb NOT NULL,
	expires_at    timestamptz,
	revoked       boolean NOT NULL DEFAULT false,
	revision      integer NOT NULL DEFAULT 1,
	created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS krun_api_keys_owner ON krun_api_keys (owner_user_id);
CREATE INDEX IF NOT EXISTS krun_api_keys_org ON krun_api_keys (org_id);

CREATE TABLE IF NOT EXISTS krun_api_key_hashes (
	key_id text PRIMARY KEY,
	hash   text NOT NULL
);
CREATE INDEX IF NOT EXISTS krun_api_key_hashes_hash ON krun_api_key_hashes (hash);

CREATE TABLE IF NOT EXISTS krun_invitations (
	source     text NOT NULL,
	subject    text NOT NULL,
	expires_at timestamptz NOT NULL,
	PRIMARY KEY (source, subject)
);
CREATE INDEX IF NOT EXISTS krun_invitations_expiry ON krun_invitations (expires_at);

CREATE TABLE IF NOT EXISTS krun_sessions (
	session_hash text PRIMARY KEY,
	user_id      text NOT NULL,
	csrf         text NOT NULL,
	auth_version integer NOT NULL,
	cli          boolean NOT NULL DEFAULT false,
	created_at   timestamptz NOT NULL,
	seen_at      timestamptz NOT NULL,
	absolute_at  timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS krun_sessions_user ON krun_sessions (user_id);
CREATE INDEX IF NOT EXISTS krun_sessions_expiry ON krun_sessions (absolute_at);

CREATE TABLE IF NOT EXISTS krun_browsers (
	browser_hash text PRIMARY KEY,
	csrf         text NOT NULL,
	generation   integer NOT NULL DEFAULT 0,
	expires_at   timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS krun_browsers_expiry ON krun_browsers (expires_at);

CREATE TABLE IF NOT EXISTS krun_login_states (
	state_hash   text PRIMARY KEY,
	provider     text NOT NULL,
	browser_hash text NOT NULL,
	fingerprint  text NOT NULL,
	verifier     text NOT NULL,
	nonce        text NOT NULL,
	return_to    text NOT NULL,
	generation   integer NOT NULL,
	expires_at   timestamptz NOT NULL,
	consumed     boolean NOT NULL DEFAULT false
);
CREATE INDEX IF NOT EXISTS krun_login_states_expiry ON krun_login_states (expires_at);

CREATE TABLE IF NOT EXISTS krun_email_logins (
	challenge_hash text PRIMARY KEY,
	provider       text NOT NULL,
	browser_hash   text NOT NULL,
	fingerprint    text NOT NULL,
	email          text NOT NULL,
	code_hash      text NOT NULL,
	return_to      text NOT NULL,
	ip             text NOT NULL,
	generation     integer NOT NULL,
	attempts       integer NOT NULL DEFAULT 0,
	sent_at        timestamptz NOT NULL,
	expires_at     timestamptz NOT NULL,
	consumed       boolean NOT NULL DEFAULT false
);
CREATE INDEX IF NOT EXISTS krun_email_logins_ip ON krun_email_logins (ip, sent_at DESC);
CREATE INDEX IF NOT EXISTS krun_email_logins_expiry ON krun_email_logins (expires_at);

CREATE TABLE IF NOT EXISTS krun_email_send_locks (
	scope      text NOT NULL,
	key_hash   text NOT NULL,
	touched_at timestamptz NOT NULL,
	PRIMARY KEY (scope, key_hash)
);
CREATE INDEX IF NOT EXISTS krun_email_send_locks_age ON krun_email_send_locks (touched_at);

CREATE TABLE IF NOT EXISTS krun_rate_limits (
	scope        text NOT NULL,
	key_hash     text NOT NULL,
	window_start timestamptz NOT NULL,
	count        integer NOT NULL CHECK (count > 0),
	PRIMARY KEY (scope, key_hash, window_start)
);
CREATE INDEX IF NOT EXISTS krun_rate_limits_window ON krun_rate_limits (window_start);

CREATE TABLE IF NOT EXISTS krun_device_codes (
	code_hash  text PRIMARY KEY,
	user_code  text NOT NULL,
	token      text NOT NULL DEFAULT '',
	expires_at timestamptz NOT NULL,
	approved   boolean NOT NULL DEFAULT false,
	consumed   boolean NOT NULL DEFAULT false
);
CREATE UNIQUE INDEX IF NOT EXISTS krun_device_codes_user_code_unique ON krun_device_codes (user_code);
CREATE INDEX IF NOT EXISTS krun_device_codes_expiry ON krun_device_codes (expires_at);

CREATE TABLE IF NOT EXISTS krun_repository_connections (
	user_id            text NOT NULL,
	provider_id        text NOT NULL,
	access_token       text NOT NULL,
	token_type         text NOT NULL DEFAULT '',
	refresh_token      text NOT NULL DEFAULT '',
	expires_at         timestamptz,
	token_revision     bigint NOT NULL DEFAULT 1,
	refresh_state      text NOT NULL DEFAULT 'ready' CONSTRAINT krun_repository_connections_refresh_state CHECK (refresh_state IN ('ready','refreshing')),
	refresh_claimed_at timestamptz,
	reconnect_required boolean NOT NULL DEFAULT false,
	connected_at       timestamptz NOT NULL DEFAULT now(),
	PRIMARY KEY (user_id, provider_id)
);

CREATE TABLE IF NOT EXISTS krun_repository_connect_states (
	state        text PRIMARY KEY,
	user_id      text NOT NULL,
	provider_id  text NOT NULL,
	org_id       text NOT NULL DEFAULT '',
	browser_hash text NOT NULL,
	expires_at   timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS krun_repository_connect_states_expiry ON krun_repository_connect_states (expires_at);

CREATE TABLE IF NOT EXISTS krun_worker_services (
	service_id          text PRIMARY KEY,
	org_id              text NOT NULL,
	network_profile     text NOT NULL DEFAULT 'default',
	repository          jsonb,
	repository_revision integer NOT NULL DEFAULT 0,
	domains_revision    integer NOT NULL DEFAULT 0,
	preview_revision    integer NOT NULL DEFAULT 0,
	active_build_count  integer NOT NULL DEFAULT 0 CHECK (active_build_count >= 0),
	updated_at          timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS krun_worker_services_org ON krun_worker_services (org_id);

CREATE TABLE IF NOT EXISTS krun_worker_service_placements (
	service_id      text NOT NULL,
	pool_id         text NOT NULL,
	org_id          text NOT NULL,
	network_profile text NOT NULL,
	role            text NOT NULL CHECK (role IN ('primary','candidate')),
	state           text NOT NULL CHECK (state IN ('preparing','ready','active','draining','failed')),
	revision        integer NOT NULL DEFAULT 1 CHECK (revision > 0),
	created_at      timestamptz NOT NULL DEFAULT now(),
	updated_at      timestamptz NOT NULL DEFAULT now(),
	PRIMARY KEY (service_id, pool_id)
);
CREATE UNIQUE INDEX IF NOT EXISTS krun_worker_service_placements_primary
	ON krun_worker_service_placements (service_id)
	WHERE role='primary' AND state IN ('preparing','ready','active');
CREATE INDEX IF NOT EXISTS krun_worker_service_placements_pool
	ON krun_worker_service_placements (pool_id, state);

CREATE TABLE IF NOT EXISTS krun_worker_service_domains (
	service_id text NOT NULL,
	domain_id  text NOT NULL,
	ordinal    integer NOT NULL DEFAULT 0,
	PRIMARY KEY (service_id, domain_id)
);

CREATE TABLE IF NOT EXISTS krun_worker_environments (
	service_id           text NOT NULL,
	env_name             text NOT NULL,
	enabled              boolean NOT NULL DEFAULT true,
	config_revision      integer NOT NULL DEFAULT 1,
	variables            jsonb NOT NULL DEFAULT '{}'::jsonb,
	release_revision     integer NOT NULL DEFAULT 0,
	release_id           text NOT NULL DEFAULT '',
	confirmed_release_id text NOT NULL DEFAULT '',
	updated_at           timestamptz NOT NULL DEFAULT now(),
	PRIMARY KEY (service_id, env_name)
);

CREATE TABLE IF NOT EXISTS krun_worker_secret_versions (
	secret_version_id text PRIMARY KEY,
	org_id            text NOT NULL,
	service_id        text NOT NULL,
	env_name          text NOT NULL,
	name              text NOT NULL,
	ciphertext        bytea NOT NULL,
	nonce             bytea NOT NULL,
	key_version       text NOT NULL,
	value_digest      text NOT NULL,
	created_by        text NOT NULL,
	created_at        timestamptz NOT NULL DEFAULT now(),
	CHECK (octet_length(ciphertext) > 16),
	CHECK (octet_length(nonce) = 12)
);
CREATE INDEX IF NOT EXISTS krun_worker_secret_versions_scope
	ON krun_worker_secret_versions (service_id, env_name, name, created_at DESC);

CREATE TABLE IF NOT EXISTS krun_worker_environment_secrets (
	service_id       text NOT NULL,
	env_name         text NOT NULL,
	name             text NOT NULL,
	secret_version_id text NOT NULL,
	PRIMARY KEY (service_id, env_name, name)
);

CREATE TABLE IF NOT EXISTS krun_worker_builds (
	build_id      text PRIMARY KEY,
	org_id        text NOT NULL,
	service_id    text NOT NULL,
	env_name      text NOT NULL,
	state         text NOT NULL,
	entrypoint    text NOT NULL DEFAULT '',
	source_digest text NOT NULL DEFAULT '',
	profile       text NOT NULL DEFAULT '',
	compatibility_date  text NOT NULL DEFAULT '',
	compatibility_flags jsonb NOT NULL DEFAULT '[]'::jsonb,
	job           text NOT NULL DEFAULT '',
	artifact_id   text NOT NULL DEFAULT '',
	error         text NOT NULL DEFAULT '',
	repository    jsonb,
	assets        jsonb,
	created_at    timestamptz NOT NULL DEFAULT now(),
	finished_at   timestamptz,
	claimed_by    text NOT NULL DEFAULT '',
	claimed_at    timestamptz
);
CREATE INDEX IF NOT EXISTS krun_worker_builds_service ON krun_worker_builds (service_id, created_at DESC);
CREATE INDEX IF NOT EXISTS krun_worker_builds_pending ON krun_worker_builds (created_at)
	WHERE state NOT IN ('succeeded', 'failed');

CREATE TABLE IF NOT EXISTS krun_worker_build_private (
	build_id           text PRIMARY KEY,
	actor_user_id      text NOT NULL,
	actor_key_id       text NOT NULL DEFAULT '',
	actor_auth_version integer NOT NULL,
	source_key         text NOT NULL DEFAULT '',
	files              jsonb,
	log                text NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS krun_worker_source_uploads (
	object_key      text PRIMARY KEY,
	org_id          text NOT NULL,
	state           text NOT NULL,
	build_id        text NOT NULL DEFAULT '',
	use_revision    bigint NOT NULL DEFAULT 0,
	created_at      timestamptz NOT NULL DEFAULT now(),
	gc_claimed_by   text NOT NULL DEFAULT '',
	gc_claimed_at   timestamptz
);
CREATE INDEX IF NOT EXISTS krun_worker_source_uploads_created ON krun_worker_source_uploads (created_at);

CREATE TABLE IF NOT EXISTS krun_worker_artifacts (
	artifact_id      text PRIMARY KEY,
	org_id           text NOT NULL,
	state            text NOT NULL,
	profile          text NOT NULL DEFAULT '',
	archive_digest   text NOT NULL DEFAULT '',
	manifest_digest  text NOT NULL DEFAULT '',
	module_digest    text NOT NULL DEFAULT '',
	compatibility_date  text NOT NULL DEFAULT '',
	compatibility_flags jsonb NOT NULL DEFAULT '[]'::jsonb,
	size_bytes       bigint NOT NULL DEFAULT 0,
	object_key       text NOT NULL DEFAULT '',
	storage_location text NOT NULL DEFAULT '',
	bucket           text NOT NULL DEFAULT '',
	created_at       timestamptz NOT NULL DEFAULT now(),
	use_revision     bigint NOT NULL DEFAULT 0,
	gc_claimed_by    text NOT NULL DEFAULT '',
	gc_claimed_at    timestamptz
);
CREATE INDEX IF NOT EXISTS krun_worker_artifacts_created ON krun_worker_artifacts (created_at);

CREATE TABLE IF NOT EXISTS krun_worker_releases (
	release_id      text PRIMARY KEY,
	org_id          text NOT NULL,
	service_id      text NOT NULL,
	env_name        text NOT NULL,
	artifact_id     text NOT NULL,
	profile         text NOT NULL DEFAULT '',
	archive_digest  text NOT NULL DEFAULT '',
	module_digest   text NOT NULL DEFAULT '',
	compatibility_date  text NOT NULL DEFAULT '',
	compatibility_flags jsonb NOT NULL DEFAULT '[]'::jsonb,
	config_revision integer NOT NULL DEFAULT 0,
	variables       jsonb NOT NULL DEFAULT '{}'::jsonb,
	secret_digest   text NOT NULL DEFAULT '',
	created_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS krun_worker_releases_service ON krun_worker_releases (service_id, env_name, created_at DESC);

CREATE TABLE IF NOT EXISTS krun_worker_release_secrets (
	release_id        text NOT NULL,
	name              text NOT NULL,
	secret_version_id text NOT NULL,
	PRIMARY KEY (release_id, name)
);

CREATE TABLE IF NOT EXISTS krun_worker_operations (
	operation_id       text PRIMARY KEY,
	kind               text NOT NULL,
	org_id             text NOT NULL,
	service_id         text NOT NULL,
	env_name           text NOT NULL,
	state              text NOT NULL,
	build_id           text NOT NULL DEFAULT '',
	release_id         text NOT NULL DEFAULT '',
	expected_revision  integer NOT NULL DEFAULT 0,
	config_revision    integer NOT NULL DEFAULT 0,
	edge_revision      bigint NOT NULL DEFAULT 0,
	release_revision   integer NOT NULL DEFAULT 0,
	error              text NOT NULL DEFAULT '',
	steps              jsonb NOT NULL DEFAULT '[]'::jsonb,
	actor_user_id      text NOT NULL DEFAULT '',
	actor_key_id       text NOT NULL DEFAULT '',
	actor_auth_version integer NOT NULL DEFAULT 0,
	created_at         timestamptz NOT NULL DEFAULT now(),
	finished_at        timestamptz,
	claimed_by         text NOT NULL DEFAULT '',
	claimed_at         timestamptz
);
CREATE INDEX IF NOT EXISTS krun_worker_operations_service ON krun_worker_operations (service_id, env_name, created_at DESC);
CREATE INDEX IF NOT EXISTS krun_worker_operations_pending ON krun_worker_operations (created_at)
	WHERE state NOT IN ('succeeded', 'failed', 'available', 'superseded');

CREATE TABLE IF NOT EXISTS krun_worker_history (
	service_id   text NOT NULL,
	env_name     text NOT NULL,
	revision     integer NOT NULL,
	release_id   text NOT NULL,
	operation_id text NOT NULL,
	kind         text NOT NULL,
	created_at   timestamptz NOT NULL DEFAULT now(),
	PRIMARY KEY (service_id, env_name, revision)
);
CREATE INDEX IF NOT EXISTS krun_worker_history_recent ON krun_worker_history (service_id, env_name, created_at DESC);

CREATE TABLE IF NOT EXISTS krun_worker_idempotency (
	idem_key   text PRIMARY KEY,
	digest     text NOT NULL,
	target_id  text NOT NULL,
	created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS krun_worker_idempotency_age ON krun_worker_idempotency (created_at);

CREATE TABLE IF NOT EXISTS worker_config_revisions (
	config_revision bigint PRIMARY KEY,
	digest          text NOT NULL,
	created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS worker_config_routes (
	host       text PRIMARY KEY,
	pool       text NOT NULL,
	path       text NOT NULL DEFAULT '',
	enabled    boolean NOT NULL,
	release_id text NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS worker_config_releases (
	release_id text NOT NULL,
	pool       text NOT NULL,
	payload    jsonb NOT NULL CHECK (jsonb_typeof(payload) = 'object'),
	PRIMARY KEY (release_id, pool)
);

CREATE TABLE IF NOT EXISTS worker_config_ops (
	config_revision bigint NOT NULL,
	ordinal         integer NOT NULL CHECK (ordinal >= 0),
	pool            text NOT NULL,
	op_type         text NOT NULL CHECK (op_type IN ('release_put','route_upsert','route_delete')),
	host            text NOT NULL DEFAULT '',
	release_id      text NOT NULL DEFAULT '',
	payload         jsonb CHECK (payload IS NULL OR jsonb_typeof(payload) = 'object'),
	PRIMARY KEY (config_revision, ordinal),
	CHECK (
		(op_type='release_put' AND host='' AND release_id<>'' AND payload IS NOT NULL AND payload->>'id'=release_id)
		OR (op_type='route_upsert' AND host<>'' AND payload IS NOT NULL AND payload->>'host'=host AND COALESCE(payload->>'release','')=release_id)
		OR (op_type='route_delete' AND host<>'' AND release_id='' AND payload IS NULL)
	)
);

CREATE TABLE IF NOT EXISTS worker_runtime_acks (
	pod_uid    text PRIMARY KEY,
	pool       text NOT NULL,
	revision   bigint NOT NULL,
	updated_at timestamptz NOT NULL
);
`

func initializeStoreSchema(ctx context.Context, pool *pgxpool.Pool) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, storeSchema); err != nil {
		return err
	}
	var incompatible bool
	if err = tx.QueryRow(ctx, `SELECT
		to_regclass(current_schema() || '.krun_schema_migrations') IS NOT NULL
		OR to_regclass(current_schema() || '.krun_control_state') IS NOT NULL
		OR to_regclass(current_schema() || '.krun_control_state_legacy') IS NOT NULL
		OR EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_schema=current_schema()
			  AND table_name IN ('krun_worker_services','krun_worker_releases')
			  AND column_name='pool'
		)
		OR EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_schema=current_schema()
			  AND table_name='worker_config_revisions'
			  AND column_name IN ('payload','ops')
		)
		OR 8 <> (
			SELECT count(*) FROM information_schema.columns
			WHERE table_schema=current_schema()
			  AND (table_name, column_name) IN (
				('krun_worker_builds','compatibility_date'),
				('krun_worker_builds','compatibility_flags'),
				('krun_worker_artifacts','profile'),
				('krun_worker_artifacts','compatibility_date'),
				('krun_worker_artifacts','compatibility_flags'),
				('krun_worker_releases','profile'),
				('krun_worker_releases','compatibility_date'),
				('krun_worker_releases','compatibility_flags')
			  )
		)`).Scan(&incompatible); err != nil {
		return err
	}
	if incompatible {
		return errors.New("unsupported pre-release database schema; recreate the database")
	}
	return tx.Commit(ctx)
}

// Retained row limits. Every table that grows per request, per build or per
// release has a bound here and a matching DELETE in retention.go.
const (
	workerBuildRetainPerService = 100
	workerBuildLogRetain        = 7 * 24 * time.Hour
	workerOperationRetain       = 30 * 24 * time.Hour
	workerArtifactRetain        = 30 * 24 * time.Hour
	workerIdempotencyRetain     = 7 * 24 * time.Hour
	workerHistoryRetainPerEnv   = 50
)
