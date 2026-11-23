-- Resource protocol v2 is additive. Protocol v1 tables remain during upgrades.
CREATE TABLE IF NOT EXISTS worker_runtime_generations (
 kind text NOT NULL,
 resource_id text NOT NULL,
 incarnation text NOT NULL DEFAULT replace(gen_random_uuid()::text,'-',''),
 generation bigint NOT NULL DEFAULT 2 CHECK(generation>0),
 changed_at timestamptz NOT NULL DEFAULT now(),
 deleted_at timestamptz,
 PRIMARY KEY(kind,resource_id)
);
CREATE INDEX IF NOT EXISTS worker_runtime_generations_deleted ON worker_runtime_generations(deleted_at) WHERE deleted_at IS NOT NULL;
CREATE TABLE IF NOT EXISTS worker_runtime_notifications (
 kind text NOT NULL,
 resource_id text NOT NULL,
 generation bigint NOT NULL,
 claimed_by text NOT NULL DEFAULT '',
 claimed_at timestamptz,
 PRIMARY KEY(kind,resource_id)
);
CREATE TABLE IF NOT EXISTS worker_runtime_route_dirty (
 worker_id text PRIMARY KEY,
 generation bigint NOT NULL DEFAULT 1
);
CREATE TABLE IF NOT EXISTS worker_runtime_bindings (
 host text NOT NULL,
 route_key text NOT NULL DEFAULT '',
 pool text NOT NULL,
 worker_id text NOT NULL,
 env_name text NOT NULL,
 match_type text NOT NULL DEFAULT '',
 path text NOT NULL DEFAULT '',
 enabled boolean NOT NULL,
 public boolean NOT NULL,
 script_name text NOT NULL DEFAULT '',
 fixed_version_id text NOT NULL DEFAULT '',
 expires_at timestamptz,
 PRIMARY KEY(host,route_key)
);
CREATE INDEX IF NOT EXISTS worker_runtime_bindings_worker ON worker_runtime_bindings(worker_id,host);
CREATE INDEX IF NOT EXISTS worker_runtime_bindings_pool ON worker_runtime_bindings(pool,host);
CREATE TABLE IF NOT EXISTS worker_runtime_resource_acks (
 pod_uid text NOT NULL,
 pool text NOT NULL,
 config_epoch text NOT NULL,
 kind text NOT NULL,
 resource_id text NOT NULL,
 incarnation text NOT NULL,
 generation bigint NOT NULL,
 updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(pod_uid,kind,resource_id)
);
CREATE INDEX IF NOT EXISTS worker_runtime_resource_acks_expiry ON worker_runtime_resource_acks(updated_at);

CREATE OR REPLACE FUNCTION krun_runtime_touch(k text, id text, removed boolean DEFAULT false, recreated boolean DEFAULT false) RETURNS void LANGUAGE plpgsql AS $$
DECLARE g bigint;
BEGIN
 IF id='' THEN RETURN; END IF;
 INSERT INTO worker_runtime_generations(kind,resource_id,deleted_at)
 VALUES(k,id,CASE WHEN removed THEN now() END)
 ON CONFLICT(kind,resource_id) DO UPDATE SET generation=worker_runtime_generations.generation+1,
 incarnation=CASE WHEN recreated OR (worker_runtime_generations.deleted_at IS NOT NULL AND NOT removed) THEN replace(gen_random_uuid()::text,'-','') ELSE worker_runtime_generations.incarnation END,
 changed_at=now(),deleted_at=excluded.deleted_at RETURNING generation INTO g;
 INSERT INTO worker_runtime_notifications(kind,resource_id,generation) VALUES(k,id,g)
 ON CONFLICT(kind,resource_id) DO UPDATE SET generation=excluded.generation,claimed_by='',claimed_at=NULL;
END $$;
CREATE OR REPLACE FUNCTION krun_runtime_route_dirty(id text) RETURNS void LANGUAGE plpgsql AS $$
BEGIN
 IF id='' THEN RETURN; END IF;
 INSERT INTO worker_runtime_route_dirty(worker_id) VALUES(id)
 ON CONFLICT(worker_id) DO UPDATE SET generation=worker_runtime_route_dirty.generation+1;
END $$;
CREATE OR REPLACE FUNCTION krun_runtime_source_changed() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE v jsonb; old_v jsonb; wid text;
BEGIN
 v:=CASE WHEN TG_OP='DELETE' THEN to_jsonb(OLD) ELSE to_jsonb(NEW) END;
 IF TG_OP<>'INSERT' THEN old_v:=to_jsonb(OLD); END IF;
 IF TG_TABLE_NAME='krun_organizations' THEN
  PERFORM krun_runtime_touch('organization',v->>'org_id',TG_OP='DELETE',TG_OP='INSERT');
  RETURN NULL;
 END IF;
 IF TG_TABLE_NAME='krun_worker_versions' THEN
  IF TG_OP IN ('INSERT','DELETE') THEN
   PERFORM krun_runtime_touch('version',v->>'version_id',TG_OP='DELETE',TG_OP='INSERT');
  END IF;
  IF TG_OP='INSERT' OR (TG_OP='DELETE' AND NOT (v->>'version_url')::boolean) THEN RETURN NULL; END IF;
 END IF;
 IF TG_TABLE_NAME='krun_resources' THEN
  IF v->>'kind'<>'worker' AND COALESCE(old_v->>'kind','')<>'worker' THEN RETURN NULL; END IF;
  wid:=v->>'resource_id';
 ELSE wid:=v->>'worker_id'; END IF;
 IF TG_TABLE_NAME IN ('krun_worker_environments','krun_worker_retained_assets') THEN
  PERFORM krun_runtime_touch('deployment',wid||'/'||(v->>'env_name'),TG_TABLE_NAME='krun_worker_environments' AND TG_OP='DELETE',TG_TABLE_NAME='krun_worker_environments' AND TG_OP='INSERT');
  IF TG_TABLE_NAME='krun_worker_environments' AND TG_OP<>'UPDATE' THEN PERFORM krun_runtime_route_dirty(wid); END IF;
 ELSE
  IF TG_TABLE_NAME NOT IN ('krun_worker_deployments','krun_worker_versions') THEN
   PERFORM krun_runtime_touch('scope',wid,TG_OP='DELETE' AND TG_TABLE_NAME IN ('krun_workers','krun_resources'),TG_OP='INSERT' AND TG_TABLE_NAME IN ('krun_workers','krun_resources'));
  END IF;
  PERFORM krun_runtime_route_dirty(wid);
 END IF;
 RETURN NULL;
END $$;
CREATE OR REPLACE FUNCTION krun_runtime_host_changed() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE h text; wid text;
BEGIN
 h:=CASE WHEN TG_OP='DELETE' THEN OLD.host ELSE NEW.host END;
 FOR wid IN SELECT DISTINCT worker_id FROM krun_worker_hostname_routes WHERE host=h
  UNION SELECT DISTINCT worker_id FROM worker_runtime_bindings WHERE host=h LOOP
  PERFORM krun_runtime_route_dirty(wid);
 END LOOP;
 IF TG_TABLE_NAME='krun_worker_hostname_routes' THEN
  IF TG_OP<>'INSERT' THEN PERFORM krun_runtime_route_dirty(OLD.worker_id); END IF;
  IF TG_OP<>'DELETE' THEN PERFORM krun_runtime_route_dirty(NEW.worker_id); END IF;
 END IF;
 RETURN NULL;
END $$;
CREATE OR REPLACE FUNCTION krun_runtime_binding_changed() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP<>'INSERT' THEN PERFORM krun_runtime_touch('route',OLD.pool||'/'||OLD.host); END IF;
 IF TG_OP<>'DELETE' THEN PERFORM krun_runtime_touch('route',NEW.pool||'/'||NEW.host); END IF;
 RETURN NULL;
END $$;
CREATE OR REPLACE TRIGGER runtime_workers AFTER INSERT OR UPDATE OR DELETE ON krun_workers FOR EACH ROW EXECUTE FUNCTION krun_runtime_source_changed();
CREATE OR REPLACE TRIGGER runtime_resources AFTER INSERT OR UPDATE OR DELETE ON krun_resources FOR EACH ROW EXECUTE FUNCTION krun_runtime_source_changed();
CREATE OR REPLACE TRIGGER runtime_orgs AFTER INSERT OR DELETE ON krun_organizations FOR EACH ROW EXECUTE FUNCTION krun_runtime_source_changed();
CREATE OR REPLACE TRIGGER runtime_environments AFTER INSERT OR DELETE OR UPDATE OF enabled,version_id,candidate_version_id,candidate_weight ON krun_worker_environments FOR EACH ROW EXECUTE FUNCTION krun_runtime_source_changed();
CREATE OR REPLACE TRIGGER runtime_retained AFTER INSERT OR UPDATE OR DELETE ON krun_worker_retained_assets FOR EACH ROW EXECUTE FUNCTION krun_runtime_source_changed();
CREATE OR REPLACE TRIGGER runtime_placements AFTER INSERT OR UPDATE OR DELETE ON krun_worker_placements FOR EACH ROW EXECUTE FUNCTION krun_runtime_source_changed();
CREATE OR REPLACE TRIGGER runtime_domains AFTER INSERT OR UPDATE OR DELETE ON krun_worker_domains FOR EACH ROW EXECUTE FUNCTION krun_runtime_source_changed();
CREATE OR REPLACE TRIGGER runtime_deployments AFTER INSERT OR UPDATE OR DELETE ON krun_worker_deployments FOR EACH ROW EXECUTE FUNCTION krun_runtime_source_changed();
CREATE OR REPLACE TRIGGER runtime_version_urls AFTER UPDATE OF version_url ON krun_worker_versions FOR EACH ROW WHEN(OLD.version_url IS DISTINCT FROM NEW.version_url) EXECUTE FUNCTION krun_runtime_source_changed();
CREATE OR REPLACE TRIGGER runtime_version_deletes AFTER INSERT OR DELETE ON krun_worker_versions FOR EACH ROW EXECUTE FUNCTION krun_runtime_source_changed();
CREATE OR REPLACE TRIGGER runtime_hostnames AFTER INSERT OR UPDATE OR DELETE ON krun_worker_hostnames FOR EACH ROW EXECUTE FUNCTION krun_runtime_host_changed();
CREATE OR REPLACE TRIGGER runtime_hostname_rules AFTER INSERT OR UPDATE OR DELETE ON krun_worker_hostname_routes FOR EACH ROW EXECUTE FUNCTION krun_runtime_host_changed();
CREATE OR REPLACE TRIGGER runtime_bindings AFTER INSERT OR UPDATE OR DELETE ON worker_runtime_bindings FOR EACH ROW EXECUTE FUNCTION krun_runtime_binding_changed();

CREATE TABLE IF NOT EXISTS worker_runtime_sessions (
 pod_uid text PRIMARY KEY,
 pool text NOT NULL,
 config_epoch text NOT NULL,
 session_id text NOT NULL,
 inventory_complete boolean NOT NULL DEFAULT false,
 connected boolean NOT NULL DEFAULT true,
 fresh boolean NOT NULL DEFAULT false,
 rejected boolean NOT NULL DEFAULT false,
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS worker_runtime_sessions_expiry ON worker_runtime_sessions(updated_at);
ALTER TABLE worker_runtime_resource_acks ADD COLUMN IF NOT EXISTS session_id text NOT NULL DEFAULT '';

-- Keep resource targets separate from v1 cursors while old controllers drain.
ALTER TABLE krun_worker_operations ADD COLUMN IF NOT EXISTS resource_generation bigint NOT NULL DEFAULT 0;
ALTER TABLE krun_worker_operations ADD COLUMN IF NOT EXISTS resource_epoch text NOT NULL DEFAULT '';
ALTER TABLE krun_worker_operations ADD COLUMN IF NOT EXISTS resource_pool text NOT NULL DEFAULT '';
