
CREATE TABLE IF NOT EXISTS krun_meta (
	key   text PRIMARY KEY,
	value text NOT NULL
);

CREATE SEQUENCE IF NOT EXISTS krun_revision_seq;
INSERT INTO krun_meta (key, value) VALUES
	('revision','0'),
	('bootstrapped','0'),
	('worker_config_head','0')
ON CONFLICT (key) DO NOTHING;

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
	email_verified boolean NOT NULL DEFAULT false,
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

CREATE TABLE IF NOT EXISTS krun_org_invitation_links (
	invitation_id text PRIMARY KEY,
	org_id text NOT NULL,
	creator_user_id text NOT NULL,
	role text NOT NULL CHECK (role IN ('member', 'viewer')),
	target_user_id text NOT NULL DEFAULT '',
	target_email text NOT NULL DEFAULT '',
	token_hash text NOT NULL UNIQUE,
	max_uses integer NOT NULL CHECK (max_uses BETWEEN 1 AND 100),
	use_count integer NOT NULL DEFAULT 0 CHECK (use_count >= 0 AND use_count <= max_uses),
	created_at timestamptz NOT NULL,
	expires_at timestamptz NOT NULL,
	revoked_at timestamptz,
	revision integer NOT NULL DEFAULT 1,
	CHECK (target_user_id = '' OR target_email = ''),
	CHECK (expires_at > created_at)
);
CREATE INDEX IF NOT EXISTS krun_org_invitation_links_org ON krun_org_invitation_links (org_id, created_at DESC, invitation_id DESC);
CREATE INDEX IF NOT EXISTS krun_org_invitation_links_expiry ON krun_org_invitation_links (expires_at);

CREATE TABLE IF NOT EXISTS krun_org_invitation_redemptions (
	invitation_id text NOT NULL,
	user_id text NOT NULL,
	joined_at timestamptz NOT NULL,
	PRIMARY KEY (invitation_id, user_id)
);

CREATE TABLE IF NOT EXISTS krun_sessions (
	session_hash text PRIMARY KEY,
	user_id      text NOT NULL,
	csrf         text NOT NULL,
	auth_version integer NOT NULL,
	cli          boolean NOT NULL DEFAULT false,
	created_at   timestamptz NOT NULL,
	seen_at      timestamptz NOT NULL,
	absolute_at  timestamptz NOT NULL,
 oauth_grant_id text NOT NULL DEFAULT '',
 oauth_scope text NOT NULL DEFAULT ''
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
 version_metadata text NOT NULL DEFAULT '',
 assets_binding text NOT NULL DEFAULT '',
	service_id           text NOT NULL,
	env_name             text NOT NULL,
	enabled              boolean NOT NULL DEFAULT true,
	config_revision      integer NOT NULL DEFAULT 1,
	variables            jsonb NOT NULL DEFAULT '{}'::jsonb,
	deployment_revision     integer NOT NULL DEFAULT 0,
	version_id           text NOT NULL DEFAULT '',
	confirmed_version_id text NOT NULL DEFAULT '',
	updated_at           timestamptz NOT NULL DEFAULT now(),
	PRIMARY KEY (service_id, env_name)
);

-- CF asset sessions expire after one hour. Each file is an individual row;
-- staging objects use the existing 24-hour source-upload GC inventory.
CREATE TABLE IF NOT EXISTS krun_worker_asset_sessions (
 session_id text PRIMARY KEY,
 org_id text NOT NULL,
 service_id text NOT NULL,
 upload_hash text NOT NULL UNIQUE,
 completion_hash text NOT NULL UNIQUE,
 created_at timestamptz NOT NULL,
 expires_at timestamptz NOT NULL CHECK (expires_at > created_at)
);
CREATE INDEX IF NOT EXISTS krun_worker_asset_sessions_expiry ON krun_worker_asset_sessions(expires_at);
CREATE TABLE IF NOT EXISTS krun_worker_asset_session_files (
 session_id text NOT NULL,
 path text NOT NULL,
 hash text NOT NULL,
 size_bytes integer NOT NULL CHECK (size_bytes >= 0 AND size_bytes <= 5242880),
 object_key text NOT NULL,
 content_type text NOT NULL DEFAULT 'application/octet-stream',
 uploaded boolean NOT NULL DEFAULT false,
 PRIMARY KEY(session_id,path)
);
CREATE INDEX IF NOT EXISTS krun_worker_asset_session_files_hash ON krun_worker_asset_session_files(session_id,hash);

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
	started_at timestamptz,
	reconcile_after timestamptz NOT NULL DEFAULT '-infinity',
	created_at    timestamptz NOT NULL DEFAULT now(),
	finished_at   timestamptz,
	claimed_by    text NOT NULL DEFAULT '',
	claimed_at    timestamptz
);
CREATE INDEX IF NOT EXISTS krun_worker_builds_service ON krun_worker_builds (service_id, created_at DESC);

CREATE TABLE IF NOT EXISTS krun_worker_build_private (
	build_id           text PRIMARY KEY,
	actor_user_id      text NOT NULL,
	actor_key_id       text NOT NULL DEFAULT '',
	actor_auth_version integer NOT NULL,
	source_key         text NOT NULL DEFAULT '',
	files              jsonb,
	log                text NOT NULL DEFAULT '',
	log_key            text NOT NULL DEFAULT ''
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

CREATE TABLE IF NOT EXISTS krun_worker_versions (
 version_metadata text NOT NULL DEFAULT '',
 assets_binding text NOT NULL DEFAULT '',
 tag text NOT NULL DEFAULT '',
 message text NOT NULL DEFAULT '',
	version_id      text PRIMARY KEY,
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
CREATE INDEX IF NOT EXISTS krun_worker_versions_service ON krun_worker_versions (service_id, env_name, created_at DESC);

CREATE TABLE IF NOT EXISTS krun_worker_version_secrets (
	version_id        text NOT NULL,
	name              text NOT NULL,
	secret_version_id text NOT NULL,
	PRIMARY KEY (version_id, name)
);

CREATE TABLE IF NOT EXISTS krun_worker_deployments (
 deployment_id text PRIMARY KEY,
 org_id text NOT NULL,
 service_id text NOT NULL,
 env_name text NOT NULL,
 version_id text NOT NULL,
 operation_id text NOT NULL UNIQUE,
 created_at timestamptz NOT NULL,
 expires_at timestamptz NOT NULL,
 committed_at timestamptz,
 CHECK (expires_at > created_at)
);
CREATE INDEX IF NOT EXISTS krun_worker_deployments_service ON krun_worker_deployments(service_id,env_name,created_at DESC);
CREATE INDEX IF NOT EXISTS krun_worker_deployments_expiry ON krun_worker_deployments(expires_at);

CREATE TABLE IF NOT EXISTS krun_worker_operations (
	operation_id       text PRIMARY KEY,
	kind               text NOT NULL,
	org_id             text NOT NULL,
	service_id         text NOT NULL,
	env_name           text NOT NULL,
	state              text NOT NULL,
	build_id           text NOT NULL DEFAULT '',
	version_id         text NOT NULL DEFAULT '',
	expected_revision  integer NOT NULL DEFAULT 0,
	config_revision    integer NOT NULL DEFAULT 0,
	edge_revision      bigint NOT NULL DEFAULT 0,
	deployment_revision   integer NOT NULL DEFAULT 0,
	error              text NOT NULL DEFAULT '',
	steps              jsonb NOT NULL DEFAULT '[]'::jsonb,
	actor_user_id      text NOT NULL DEFAULT '',
	actor_key_id       text NOT NULL DEFAULT '',
	actor_auth_version integer NOT NULL DEFAULT 0,
	started_at timestamptz,
	reconcile_after timestamptz NOT NULL DEFAULT '-infinity',
	created_at         timestamptz NOT NULL DEFAULT now(),
	finished_at        timestamptz,
	claimed_by         text NOT NULL DEFAULT '',
	claimed_at         timestamptz
);
CREATE INDEX IF NOT EXISTS krun_worker_operations_service ON krun_worker_operations (service_id, env_name, created_at DESC);

CREATE TABLE IF NOT EXISTS krun_worker_history (
	service_id   text NOT NULL,
	env_name     text NOT NULL,
	revision     integer NOT NULL,
	version_id   text NOT NULL,
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
 expires_at timestamptz,
 service_id text NOT NULL,
 shard text NOT NULL CHECK(shard ~ '^[0-9a-f]{2}$'),
	host       text PRIMARY KEY,
	pool       text NOT NULL,
	path       text NOT NULL DEFAULT '',
	enabled    boolean NOT NULL,
	version_id text NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS worker_config_versions (
	version_id text NOT NULL,
	pool       text NOT NULL,
	payload    jsonb NOT NULL CHECK (jsonb_typeof(payload) = 'object'),
	PRIMARY KEY (version_id, pool)
);

CREATE TABLE IF NOT EXISTS worker_config_ops (
	config_revision bigint NOT NULL,
	ordinal         integer NOT NULL CHECK (ordinal >= 0),
	pool            text NOT NULL,
	op_type         text NOT NULL CHECK (op_type IN ('version_put','route_upsert','route_delete')),
	host            text NOT NULL DEFAULT '',
	version_id      text NOT NULL DEFAULT '',
	payload         jsonb CHECK (payload IS NULL OR jsonb_typeof(payload) = 'object'),
	PRIMARY KEY (config_revision, ordinal),
	CHECK (
		(op_type='version_put' AND host='' AND version_id<>'' AND payload IS NOT NULL AND payload->>'id'=version_id)
		OR (op_type='route_upsert' AND host<>'' AND payload IS NOT NULL AND payload->>'host'=host AND COALESCE(payload->>'version','')=version_id)
		OR (op_type='route_delete' AND host<>'' AND version_id='' AND payload IS NULL)
	)
);

CREATE INDEX IF NOT EXISTS worker_config_routes_service ON worker_config_routes(service_id);
CREATE INDEX IF NOT EXISTS worker_config_routes_version ON worker_config_routes(version_id,pool);
CREATE INDEX IF NOT EXISTS worker_config_routes_shard ON worker_config_routes(pool,shard,host);
CREATE INDEX IF NOT EXISTS worker_config_ops_pool ON worker_config_ops(pool,config_revision,ordinal);
CREATE INDEX IF NOT EXISTS krun_worker_versions_artifact ON krun_worker_versions(artifact_id,service_id);
CREATE INDEX IF NOT EXISTS krun_resources_page ON krun_resources(org_id,kind,resource_id);
CREATE INDEX IF NOT EXISTS krun_worker_idempotency_prefix ON krun_worker_idempotency(idem_key text_pattern_ops);
CREATE INDEX IF NOT EXISTS krun_worker_builds_schedulable ON krun_worker_builds(reconcile_after,created_at) WHERE state NOT IN ('succeeded','failed');
CREATE INDEX IF NOT EXISTS krun_worker_operations_schedulable ON krun_worker_operations(reconcile_after,created_at) WHERE state NOT IN ('succeeded','failed','available','superseded');
CREATE TABLE IF NOT EXISTS worker_asset_objects(
 object_key text PRIMARY KEY,
 artifact_id text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS worker_asset_objects_artifact ON worker_asset_objects(artifact_id,object_key);
CREATE SEQUENCE IF NOT EXISTS worker_dirty_seq;
CREATE TABLE IF NOT EXISTS worker_retention_scopes(service_id text PRIMARY KEY,generation bigint NOT NULL DEFAULT nextval('worker_dirty_seq'));
CREATE OR REPLACE FUNCTION krun_mark_worker_retention() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 INSERT INTO worker_retention_scopes(service_id) VALUES(NEW.service_id) ON CONFLICT(service_id) DO UPDATE SET generation=nextval('worker_dirty_seq');
 RETURN NULL;
END $$;
CREATE OR REPLACE TRIGGER worker_build_retention AFTER INSERT OR UPDATE OF state ON krun_worker_builds FOR EACH ROW EXECUTE FUNCTION krun_mark_worker_retention();
CREATE OR REPLACE TRIGGER worker_history_retention AFTER INSERT ON krun_worker_history FOR EACH ROW EXECUTE FUNCTION krun_mark_worker_retention();
CREATE INDEX IF NOT EXISTS worker_retention_scopes_generation ON worker_retention_scopes(generation,service_id);
CREATE INDEX IF NOT EXISTS krun_worker_versions_age ON krun_worker_versions(created_at);
CREATE INDEX IF NOT EXISTS krun_worker_operations_age ON krun_worker_operations(created_at);
CREATE INDEX IF NOT EXISTS krun_worker_builds_age ON krun_worker_builds(created_at);
CREATE INDEX IF NOT EXISTS krun_worker_environments_version ON krun_worker_environments(version_id);
CREATE INDEX IF NOT EXISTS krun_worker_environments_confirmed ON krun_worker_environments(confirmed_version_id);
CREATE INDEX IF NOT EXISTS krun_worker_history_version ON krun_worker_history(version_id);
CREATE INDEX IF NOT EXISTS krun_worker_operations_version ON krun_worker_operations(version_id);
CREATE INDEX IF NOT EXISTS krun_worker_builds_artifact ON krun_worker_builds(artifact_id);
CREATE INDEX IF NOT EXISTS krun_worker_environment_secrets_version ON krun_worker_environment_secrets(secret_version_id);
CREATE INDEX IF NOT EXISTS krun_worker_version_secrets_version ON krun_worker_version_secrets(secret_version_id);
CREATE TABLE IF NOT EXISTS worker_config_dirty (
 service_id text PRIMARY KEY,
 generation bigint NOT NULL DEFAULT nextval('worker_dirty_seq')
);
CREATE INDEX IF NOT EXISTS worker_config_dirty_generation ON worker_config_dirty(generation,service_id);
CREATE TABLE IF NOT EXISTS worker_config_checkpoints (
 pool text NOT NULL,
 shard text NOT NULL,
 revision bigint NOT NULL,
 published_revision bigint NOT NULL DEFAULT 0,
 PRIMARY KEY(pool,shard)
);
CREATE OR REPLACE FUNCTION krun_mark_worker_config_dirty() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP<>'INSERT' THEN
  INSERT INTO worker_config_dirty(service_id) VALUES(OLD.service_id) ON CONFLICT(service_id) DO UPDATE SET generation=nextval('worker_dirty_seq');
 END IF;
 IF TG_OP<>'DELETE' THEN
  INSERT INTO worker_config_dirty(service_id) VALUES(NEW.service_id) ON CONFLICT(service_id) DO UPDATE SET generation=nextval('worker_dirty_seq');
 END IF;
 RETURN NULL;
END $$;
CREATE OR REPLACE TRIGGER worker_services_config_dirty AFTER INSERT OR UPDATE OR DELETE ON krun_worker_services FOR EACH ROW EXECUTE FUNCTION krun_mark_worker_config_dirty();
CREATE OR REPLACE TRIGGER worker_env_config_dirty AFTER INSERT OR DELETE OR UPDATE OF enabled,version_id ON krun_worker_environments FOR EACH ROW EXECUTE FUNCTION krun_mark_worker_config_dirty();
CREATE OR REPLACE TRIGGER worker_domains_config_dirty AFTER INSERT OR UPDATE OR DELETE ON krun_worker_service_domains FOR EACH ROW EXECUTE FUNCTION krun_mark_worker_config_dirty();
CREATE OR REPLACE TRIGGER worker_placements_config_dirty AFTER INSERT OR UPDATE OR DELETE ON krun_worker_service_placements FOR EACH ROW EXECUTE FUNCTION krun_mark_worker_config_dirty();
CREATE OR REPLACE FUNCTION krun_mark_resource_config_dirty() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP<>'INSERT' AND OLD.kind='service' THEN INSERT INTO worker_retention_scopes(service_id) VALUES(OLD.resource_id) ON CONFLICT(service_id) DO UPDATE SET generation=nextval('worker_dirty_seq');INSERT INTO worker_config_dirty(service_id) VALUES(OLD.resource_id) ON CONFLICT(service_id) DO UPDATE SET generation=nextval('worker_dirty_seq');END IF;
 IF TG_OP<>'DELETE' AND NEW.kind='service' THEN INSERT INTO worker_config_dirty(service_id) VALUES(NEW.resource_id) ON CONFLICT(service_id) DO UPDATE SET generation=nextval('worker_dirty_seq');END IF;
 RETURN NULL;
END $$;
CREATE OR REPLACE TRIGGER resource_config_dirty AFTER INSERT OR UPDATE OR DELETE ON krun_resources FOR EACH ROW EXECUTE FUNCTION krun_mark_resource_config_dirty();
CREATE TABLE IF NOT EXISTS worker_runtime_acks (
	pod_uid    text PRIMARY KEY,
	pool       text NOT NULL,
	revision   bigint NOT NULL,
	updated_at timestamptz NOT NULL
);

CREATE OR REPLACE TRIGGER deployment_config_dirty AFTER INSERT OR UPDATE OR DELETE ON krun_worker_deployments FOR EACH ROW EXECUTE FUNCTION krun_mark_worker_config_dirty();

-- Cloudflare protocol identities are stable and independent of display names.
CREATE TABLE IF NOT EXISTS krun_worker_scripts (
 org_id text NOT NULL,
 script_name text NOT NULL,
 service_id text NOT NULL UNIQUE,
 workers_dev boolean NOT NULL DEFAULT true,
 previews_enabled boolean NOT NULL DEFAULT true,
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(org_id,script_name)
);

CREATE TABLE IF NOT EXISTS krun_oauth_authorizations (
 auth_hash text PRIMARY KEY,
 client_id text NOT NULL,
 redirect_uri text NOT NULL DEFAULT '',
 challenge text NOT NULL DEFAULT '',
 state text NOT NULL DEFAULT '',
 scope text NOT NULL,
 user_code text UNIQUE,
 decision text NOT NULL DEFAULT 'pending' CHECK(decision IN('pending','approved','denied')),
 user_id text NOT NULL DEFAULT '',
 auth_version integer NOT NULL DEFAULT 0,
 code_hash text UNIQUE,
 created_at timestamptz NOT NULL,
 expires_at timestamptz NOT NULL,
 consumed boolean NOT NULL DEFAULT false,
 CHECK(expires_at > created_at)
);
CREATE INDEX IF NOT EXISTS krun_oauth_authorizations_expiry ON krun_oauth_authorizations(expires_at);
CREATE TABLE IF NOT EXISTS krun_oauth_grants (
 grant_id text PRIMARY KEY,
 user_id text NOT NULL,
 client_id text NOT NULL,
 scope text NOT NULL,
 auth_version integer NOT NULL,
 created_at timestamptz NOT NULL,
 expires_at timestamptz NOT NULL,
 revoked boolean NOT NULL DEFAULT false,
 CHECK(expires_at > created_at)
);
CREATE INDEX IF NOT EXISTS krun_oauth_grants_user ON krun_oauth_grants(user_id);
CREATE INDEX IF NOT EXISTS krun_oauth_grants_expiry ON krun_oauth_grants(expires_at);
CREATE TABLE IF NOT EXISTS krun_oauth_refresh_tokens (
 token_hash text PRIMARY KEY,
 grant_id text NOT NULL,
 created_at timestamptz NOT NULL,
 expires_at timestamptz NOT NULL,
 consumed boolean NOT NULL DEFAULT false,
 CHECK(expires_at > created_at)
);
CREATE INDEX IF NOT EXISTS krun_oauth_refresh_tokens_grant ON krun_oauth_refresh_tokens(grant_id);
CREATE INDEX IF NOT EXISTS krun_oauth_refresh_tokens_expiry ON krun_oauth_refresh_tokens(expires_at);

-- Logical buckets are durable resources, not per-request journals. Objects are
-- stored remotely; dropping a Worker or binding never deletes this resource.
CREATE TABLE IF NOT EXISTS krun_worker_buckets (
 bucket_id text PRIMARY KEY,
 org_id text NOT NULL,
 name text NOT NULL,
 store_id text NOT NULL,
 enabled boolean NOT NULL DEFAULT true,
 revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
 created_at timestamptz NOT NULL,
 UNIQUE (org_id, name)
);
CREATE INDEX IF NOT EXISTS krun_worker_buckets_org ON krun_worker_buckets(org_id, bucket_id);
CREATE TABLE IF NOT EXISTS krun_worker_environment_r2 (
 service_id text NOT NULL,
 env_name text NOT NULL,
 name text NOT NULL,
 bucket_id text NOT NULL,
 PRIMARY KEY (service_id, env_name, name)
);
CREATE INDEX IF NOT EXISTS krun_worker_environment_r2_bucket ON krun_worker_environment_r2(bucket_id);
CREATE TABLE IF NOT EXISTS krun_worker_version_r2 (
 version_id text NOT NULL,
 name text NOT NULL,
 bucket_id text NOT NULL,
 PRIMARY KEY (version_id, name)
);
CREATE INDEX IF NOT EXISTS krun_worker_version_r2_bucket ON krun_worker_version_r2(bucket_id);
