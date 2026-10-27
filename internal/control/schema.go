package control

import "time"

// storeSchema is the normalized control-plane schema. It replaces the single
// krun_control_state jsonb document. Every statement is idempotent so startup
// can apply it repeatedly.
//
// No foreign keys: reference integrity comes from write-path validation,
// explicit cascade deletes in the same statement batch, and reconciliation
// queries for orphans. See AGENTS.md section 9.
const storeSchema = `
CREATE TABLE IF NOT EXISTS krun_meta (
	key   text PRIMARY KEY,
	value text NOT NULL
);

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
	repository          jsonb,
	repository_revision integer NOT NULL DEFAULT 0,
	domains_revision    integer NOT NULL DEFAULT 0,
	preview_revision    integer NOT NULL DEFAULT 0,
	active_build_count  integer NOT NULL DEFAULT 0 CHECK (active_build_count >= 0),
	updated_at          timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS krun_worker_services_org ON krun_worker_services (org_id);

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

CREATE TABLE IF NOT EXISTS krun_worker_builds (
	build_id      text PRIMARY KEY,
	org_id        text NOT NULL,
	service_id    text NOT NULL,
	env_name      text NOT NULL,
	state         text NOT NULL,
	entrypoint    text NOT NULL DEFAULT '',
	source_digest text NOT NULL DEFAULT '',
	profile       text NOT NULL DEFAULT '',
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
	archive_digest   text NOT NULL DEFAULT '',
	manifest_digest  text NOT NULL DEFAULT '',
	module_digest    text NOT NULL DEFAULT '',
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
	archive_digest  text NOT NULL DEFAULT '',
	module_digest   text NOT NULL DEFAULT '',
	config_revision integer NOT NULL DEFAULT 0,
	variables       jsonb NOT NULL DEFAULT '{}'::jsonb,
	pool            text NOT NULL DEFAULT 'default',
	created_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS krun_worker_releases_service ON krun_worker_releases (service_id, env_name, created_at DESC);

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
`

// storeSchemaVersion mirrors the newest successfully applicable migration.
// The ledger, not this diagnostic value, is authoritative.
const storeSchemaVersion = "14"

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
