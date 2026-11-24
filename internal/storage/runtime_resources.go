package storage

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/krun-dev/krun/internal/domain"
)

type runtimeQueryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func (s *Store) RuntimeEpoch(ctx context.Context) (string, error) {
	var epoch string
	err := s.pool.QueryRow(ctx, `SELECT value FROM krun_meta WHERE key='worker_config_epoch'`).Scan(&epoch)
	return epoch, err
}

func runtimeDefaultRef(ref domain.ResourceRef) domain.ResourceRef {
	digest := md5.Sum([]byte(ref.Key()))
	ref.Incarnation = hex.EncodeToString(digest[:])
	ref.Generation = 1
	return ref
}

func readRuntimeGenerations(ctx context.Context, db runtimeQueryer, refs []domain.ResourceRef) (map[string]domain.ResourceRef, error) {
	kinds := make([]string, 0, len(refs))
	ids := make([]string, 0, len(refs))
	out := make(map[string]domain.ResourceRef, len(refs))
	for _, ref := range refs {
		kinds = append(kinds, ref.Kind)
		ids = append(ids, ref.ID)
		out[ref.Key()] = runtimeDefaultRef(ref)
	}
	rows, err := db.Query(ctx, `SELECT g.kind,g.resource_id,g.incarnation,g.generation FROM worker_runtime_generations g
		JOIN unnest($1::text[],$2::text[]) AS wanted(kind,id) ON g.kind=wanted.kind AND g.resource_id=wanted.id`, kinds, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var ref domain.ResourceRef
		if err := rows.Scan(&ref.Kind, &ref.ID, &ref.Incarnation, &ref.Generation); err != nil {
			return nil, err
		}
		out[ref.Key()] = ref
	}
	return out, rows.Err()
}

func (s *Store) RuntimeGenerations(ctx context.Context, refs []domain.ResourceRef) (string, map[string]domain.ResourceRef, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return "", nil, err
	}
	defer tx.Rollback(ctx)
	var epoch string
	if err = tx.QueryRow(ctx, `SELECT value FROM krun_meta WHERE key='worker_config_epoch'`).Scan(&epoch); err != nil {
		return "", nil, err
	}
	refsByKey, err := readRuntimeGenerations(ctx, tx, refs)
	if err != nil {
		return "", nil, err
	}
	return epoch, refsByKey, tx.Commit(ctx)
}

// RuntimeResources expands one bounded dependency graph in a repeatable-read
// view. Bindings are stable; deployments and qualification are read directly
// from source rows, never from the legacy global revision projection.
func (s *Store) RuntimeResources(ctx context.Context, pool string, refs []domain.ResourceRef) (string, []domain.RuntimeResource, error) {
	if len(refs) == 0 || len(refs) > 256 {
		return "", nil, domain.Invalid("CONFIG_RESOURCES_INVALID", "Between 1 and 256 resources required")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return "", nil, err
	}
	defer tx.Rollback(ctx)
	var epoch string
	if err = tx.QueryRow(ctx, `SELECT value FROM krun_meta WHERE key='worker_config_epoch'`).Scan(&epoch); err != nil {
		return "", nil, err
	}
	seen := map[string]bool{}
	resources := []domain.RuntimeResource{}
	for len(refs) > 0 {
		if len(seen)+len(refs) > 16384 {
			return "", nil, domain.Invalid("CONFIG_RESOURCE_TOO_LARGE", "Resource dependency limit exceeded")
		}
		pending := []domain.ResourceRef{}
		for _, ref := range refs {
			if !seen[ref.Key()] {
				seen[ref.Key()] = true
				pending = append(pending, ref)
			}
		}
		versions, err := readRuntimeGenerations(ctx, tx, pending)
		if err != nil {
			return "", nil, err
		}
		batch, err := loadRuntimeResources(ctx, tx, pool, pending, versions)
		if err != nil {
			return "", nil, err
		}
		refs = nil
		for _, resource := range batch {
			resources = append(resources, resource)
			refs = append(refs, resource.Dependencies...)
		}
	}
	return epoch, resources, tx.Commit(ctx)
}

func loadRuntimeResources(ctx context.Context, tx pgx.Tx, pool string, refs []domain.ResourceRef, versions map[string]domain.ResourceRef) ([]domain.RuntimeResource, error) {
	groups := map[string][]string{}
	values := map[string]domain.RuntimeResource{}
	for _, ref := range refs {
		if len(ref.ID) > 512 || ref.ID == "" {
			return nil, domain.Invalid("CONFIG_RESOURCE_INVALID", "Invalid resource identifier")
		}
		groups[ref.Kind] = append(groups[ref.Kind], ref.ID)
		values[ref.Key()] = domain.RuntimeResource{ResourceRef: versions[ref.Key()], Deleted: true}
	}
	put := func(ref domain.ResourceRef, payload any, dependencies ...domain.ResourceRef) error {
		raw, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		values[ref.Key()] = domain.RuntimeResource{ResourceRef: versions[ref.Key()], Payload: raw, Dependencies: dependencies}
		return nil
	}
	if ids := groups["route"]; len(ids) > 0 {
		hosts := []string{}
		for _, id := range ids {
			prefix, host, ok := strings.Cut(id, "/")
			if !ok || prefix != pool || host == "" ||
				len(host) > domain.RuntimeRouteHostMaxBytes || strings.ContainsAny(host, "/\\: ") {
				return nil, domain.Forbidden()
			}
			hosts = append(hosts, host)
		}
		rows, err := tx.Query(ctx, `SELECT worker_id,host,pool,path,enabled,fixed_version_id,expires_at,script_name,env_name,public,match_type
			FROM worker_runtime_bindings WHERE pool=$1 AND host=ANY($2) ORDER BY host,route_key`, pool, hosts)
		if err != nil {
			return nil, err
		}
		bindings := map[string][]domain.RuntimeRouteBinding{}
		deps := map[string][]domain.ResourceRef{}
		for rows.Next() {
			var r domain.RuntimeRouteBinding
			if err = rows.Scan(&r.Worker, &r.Host, &r.Pool, &r.Path, &r.Enabled, &r.Version, &r.Expires, &r.ScriptName, &r.Environment, &r.Public, &r.Match); err != nil {
				rows.Close()
				return nil, err
			}
			r.Fixed = r.Version != ""
			bindings[r.Host] = append(bindings[r.Host], r)
			deps[r.Host] = append(deps[r.Host], domain.ResourceRef{Kind: "scope", ID: r.Worker})
			deps[r.Host] = append(deps[r.Host], domain.ResourceRef{Kind: "deployment", ID: r.Worker + "/" + r.Environment})
			if r.Fixed {
				deps[r.Host] = append(deps[r.Host], domain.ResourceRef{Kind: "version", ID: r.Version})
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		for host, routes := range bindings {
			if err = put(domain.ResourceRef{Kind: "route", ID: pool + "/" + host}, routes, deps[host]...); err != nil {
				return nil, err
			}
		}
	}
	if ids := groups["deployment"]; len(ids) > 0 {
		workers := make([]string, 0, len(ids))
		environments := make([]string, 0, len(ids))
		for _, id := range ids {
			worker, environment, ok := strings.Cut(id, "/")
			if !ok {
				return nil, domain.Invalid("CONFIG_RESOURCE_INVALID", "Invalid deployment identifier")
			}
			workers = append(workers, worker)
			environments = append(environments, environment)
		}
		rows, err := tx.Query(ctx, `SELECT e.worker_id,e.env_name,e.enabled,e.version_id,e.candidate_version_id,e.candidate_weight,
			COALESCE((SELECT jsonb_agg(jsonb_build_object('version_id',a.version_id,'expires_at',a.expires_at) ORDER BY a.version_id)
			FROM krun_worker_retained_assets a WHERE a.worker_id=e.worker_id AND a.env_name=e.env_name AND a.expires_at>now()),'[]'::jsonb)
			FROM krun_worker_environments e JOIN unnest($1::text[],$2::text[]) AS wanted(worker_id,env_name)
			ON e.worker_id=wanted.worker_id AND e.env_name=wanted.env_name WHERE true
			AND EXISTS(SELECT 1 FROM krun_worker_placements p JOIN krun_workers w ON w.worker_id=p.worker_id AND w.org_id=p.org_id AND w.network_profile=p.network_profile JOIN krun_resources r ON r.resource_id=p.worker_id AND r.org_id=p.org_id AND r.kind='worker'
			JOIN krun_organizations o ON o.org_id=p.org_id WHERE p.worker_id=e.worker_id AND p.pool_id=$3 AND p.state IN ('preparing','ready','active'))`, workers, environments, pool)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var d domain.RuntimeDeployment
			if err = rows.Scan(&d.Worker, &d.Environment, &d.Enabled, &d.Version, &d.Candidate, &d.CandidateWeight, &d.Retained); err != nil {
				rows.Close()
				return nil, err
			}
			deps := []domain.ResourceRef{{Kind: "scope", ID: d.Worker}}
			for _, id := range append([]string{d.Version, d.Candidate}, retainedIDs(d.Retained)...) {
				if id != "" {
					deps = append(deps, domain.ResourceRef{Kind: "version", ID: id})
				}
			}
			if err = put(domain.ResourceRef{Kind: "deployment", ID: d.Worker + "/" + d.Environment}, d, deps...); err != nil {
				rows.Close()
				return nil, err
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	if ids := groups["scope"]; len(ids) > 0 {
		rows, err := tx.Query(ctx, `SELECT w.worker_id,w.org_id FROM krun_workers w JOIN krun_resources r ON r.resource_id=w.worker_id AND r.org_id=w.org_id AND r.kind='worker'
			JOIN krun_organizations o ON o.org_id=w.org_id WHERE w.worker_id=ANY($1) AND EXISTS(SELECT 1 FROM krun_worker_placements p
			WHERE p.worker_id=w.worker_id AND p.org_id=w.org_id AND p.network_profile=w.network_profile AND p.pool_id=$2 AND p.state IN ('preparing','ready','active'))`, ids, pool)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id, org string
			if err = rows.Scan(&id, &org); err != nil {
				rows.Close()
				return nil, err
			}
			if err = put(domain.ResourceRef{Kind: "scope", ID: id}, domain.RuntimeScope{Allowed: true}, domain.ResourceRef{Kind: "organization", ID: org}); err != nil {
				rows.Close()
				return nil, err
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	if ids := groups["organization"]; len(ids) > 0 {
		rows, err := tx.Query(ctx, `SELECT DISTINCT o.org_id FROM krun_organizations o JOIN krun_worker_placements p ON p.org_id=o.org_id
			WHERE o.org_id=ANY($1) AND p.pool_id=$2 AND p.state IN ('preparing','ready','active')`, ids, pool)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			if err = put(domain.ResourceRef{Kind: "organization", ID: id}, domain.RuntimeScope{Allowed: true}); err != nil {
				rows.Close()
				return nil, err
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	if ids := groups["version"]; len(ids) > 0 {
		rows, err := tx.Query(ctx, `SELECT `+workerVersionColumns+` FROM krun_worker_versions v WHERE version_id=ANY($1) AND EXISTS(
			SELECT 1 FROM krun_worker_placements p JOIN krun_workers w ON w.worker_id=p.worker_id AND w.org_id=p.org_id AND w.network_profile=p.network_profile JOIN krun_resources r ON r.resource_id=p.worker_id AND r.org_id=p.org_id AND r.kind='worker'
			JOIN krun_organizations o ON o.org_id=p.org_id WHERE p.worker_id=v.worker_id AND p.org_id=v.org_id AND p.pool_id=$2 AND p.state IN ('preparing','ready','active'))`, ids, pool)
		if err != nil {
			return nil, err
		}
		st := domain.NewWorkerState()
		err = readVersionRows(rows, st)
		rows.Close()
		if err != nil {
			return nil, err
		}
		if err = loadVersionStorage(ctx, tx, st.Versions); err != nil {
			return nil, err
		}
		for id, v := range st.Versions {
			if err = put(domain.ResourceRef{Kind: "version", ID: id}, domain.RuntimeVersionDescriptor(v), domain.ResourceRef{Kind: "scope", ID: v.Worker}); err != nil {
				return nil, err
			}
		}
	}
	out := make([]domain.RuntimeResource, 0, len(refs))
	for _, ref := range refs {
		if _, ok := map[string]bool{"route": true, "deployment": true, "scope": true, "organization": true, "version": true}[ref.Kind]; !ok {
			return nil, domain.Invalid("CONFIG_RESOURCE_INVALID", "Unknown resource type")
		}
		out = append(out, values[ref.Key()])
	}
	return out, nil
}

func retainedIDs(retained []domain.RetainedWorkerVersion) []string {
	out := make([]string, 0, len(retained))
	for _, r := range retained {
		out = append(out, r.Version)
	}
	return out
}

func (s *Store) RuntimeRouteDirty(ctx context.Context, limit int) ([]DirtyWorker, error) {
	priority, _ := ctx.Value(WorkerConfigPriorityKey{}).(string)
	query := `SELECT worker_id,generation FROM worker_runtime_route_dirty ORDER BY worker_id LIMIT $1`
	args := []any{max(0, min(limit, 128))}
	if priority != "" {
		// A deployment publishes only its own binding intent. Catalogue
		// rebuilds advance in independent bounded background batches.
		query = `SELECT worker_id,generation FROM worker_runtime_route_dirty WHERE worker_id=$1`
		args = []any{priority}
	}
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return pgx.CollectRows(rows, pgx.RowToStructByPos[DirtyWorker])
}

func (s *Store) EnsureRuntimeCatalogue(ctx context.Context, digest string) error {
	_, err := s.pool.Exec(ctx, `WITH changed AS (INSERT INTO krun_meta(key,value) VALUES('worker_runtime_catalogue',$1)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value WHERE krun_meta.value IS DISTINCT FROM excluded.value RETURNING 1)
		INSERT INTO worker_runtime_route_dirty(worker_id) SELECT worker_id FROM krun_workers WHERE EXISTS(SELECT 1 FROM changed)
		ON CONFLICT(worker_id) DO UPDATE SET generation=worker_runtime_route_dirty.generation+1`, digest)
	return err
}

func (s *Store) PublishRuntimeBindings(ctx context.Context, dirty DirtyWorker, bindings []domain.RuntimeRouteBinding) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `DELETE FROM worker_runtime_route_dirty WHERE worker_id=$1 AND generation=$2`, dirty.ID, dirty.Generation)
	if err != nil || tag.RowsAffected() == 0 {
		return false, err
	}
	// Include every rule on a shared host, including old references after reassignment.
	rows, err := tx.Query(ctx, `WITH hosts AS(SELECT host FROM krun_worker_hostname_routes WHERE worker_id=$1 UNION SELECT host FROM worker_runtime_bindings WHERE worker_id=$1 AND route_key<>'')
		SELECT host,route_key FROM worker_runtime_bindings WHERE worker_id=$1 OR(route_key<>'' AND host IN(SELECT host FROM hosts))`, dirty.ID)
	if err != nil {
		return false, err
	}
	old := map[string][2]string{}
	for rows.Next() {
		var h, k string
		if err = rows.Scan(&h, &k); err != nil {
			rows.Close()
			return false, err
		}
		old[h+"\x00"+k] = [2]string{h, k}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return false, err
	}
	for _, r := range bindings {
		delete(old, r.Host+"\x00"+r.Key())
		if _, err = tx.Exec(ctx, `INSERT INTO worker_runtime_bindings(host,route_key,pool,worker_id,env_name,match_type,path,enabled,public,script_name,fixed_version_id,expires_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
			ON CONFLICT(host,route_key) DO UPDATE SET pool=excluded.pool,worker_id=excluded.worker_id,env_name=excluded.env_name,
			match_type=excluded.match_type,path=excluded.path,enabled=excluded.enabled,public=excluded.public,script_name=excluded.script_name,
			fixed_version_id=excluded.fixed_version_id,expires_at=excluded.expires_at
			WHERE (worker_runtime_bindings.pool,worker_runtime_bindings.worker_id,worker_runtime_bindings.env_name,worker_runtime_bindings.match_type,
			worker_runtime_bindings.path,worker_runtime_bindings.enabled,worker_runtime_bindings.public,worker_runtime_bindings.script_name,worker_runtime_bindings.fixed_version_id,worker_runtime_bindings.expires_at)
			IS DISTINCT FROM (excluded.pool,excluded.worker_id,excluded.env_name,excluded.match_type,excluded.path,excluded.enabled,excluded.public,excluded.script_name,excluded.fixed_version_id,excluded.expires_at)`,
			r.Host, r.Key(), r.Pool, r.Worker, r.Environment, r.Match, r.Path, r.Enabled, r.Public, r.ScriptName, r.Version, r.Expires); err != nil {
			return false, err
		}
	}
	for _, key := range old {
		if _, err = tx.Exec(ctx, `DELETE FROM worker_runtime_bindings WHERE host=$1 AND route_key=$2`, key[0], key[1]); err != nil {
			return false, err
		}
	}
	return true, tx.Commit(ctx)
}

// DispatchRuntimeNotifications is an outbox relay, not a subscriber consumer
// group. Every LISTEN connection receives the committed hints; missed hints are
// repaired by authoritative generation checks.
func (s *Store) DispatchRuntimeNotifications(ctx context.Context, replica string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `UPDATE worker_runtime_notifications SET claimed_by=$1,claimed_at=now()
		WHERE(kind,resource_id) IN(SELECT kind,resource_id FROM worker_runtime_notifications WHERE claimed_at IS NULL OR claimed_at<now()-interval '10 seconds' ORDER BY kind,resource_id LIMIT 128 FOR UPDATE SKIP LOCKED)
		RETURNING kind,resource_id,generation`, replica)
	if err != nil {
		return err
	}
	refs := []domain.ResourceRef{}
	for rows.Next() {
		var r domain.ResourceRef
		if err = rows.Scan(&r.Kind, &r.ID, &r.Generation); err != nil {
			rows.Close()
			return err
		}
		refs = append(refs, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	for _, r := range refs {
		raw, _ := json.Marshal(r)
		if _, err = s.pool.Exec(ctx, `SELECT pg_notify('krun_runtime_resources',$1)`, string(raw)); err != nil {
			return err
		}
		if _, err = s.pool.Exec(ctx, `DELETE FROM worker_runtime_notifications WHERE kind=$1 AND resource_id=$2 AND generation=$3 AND claimed_by=$4`, r.Kind, r.ID, r.Generation, replica); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ListenRuntimeNotifications(ctx context.Context, changed func(domain.ResourceRef)) error {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err = conn.Exec(ctx, `LISTEN krun_runtime_resources`); err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		_, _ = conn.Exec(cleanup, `UNLISTEN krun_runtime_resources`)
	}()
	for {
		notification, err := conn.Conn().WaitForNotification(ctx)
		if err != nil {
			return err
		}
		var ref domain.ResourceRef
		if json.Unmarshal([]byte(notification.Payload), &ref) == nil {
			changed(ref)
		}
	}
}

func (s *Store) CleanupRuntimeResources(ctx context.Context) error {
	for _, query := range []string{
		`DELETE FROM worker_runtime_resource_acks a WHERE a.ctid IN(SELECT a.ctid FROM worker_runtime_resource_acks a WHERE a.updated_at<now()-interval '1 minute' AND NOT EXISTS(SELECT 1 FROM worker_runtime_sessions s WHERE s.pod_uid=a.pod_uid AND s.session_id=a.session_id AND s.connected AND s.updated_at>now()-interval '1 minute') LIMIT 1000)`,
		`DELETE FROM worker_runtime_sessions WHERE ctid IN(SELECT ctid FROM worker_runtime_sessions WHERE updated_at<now()-interval '1 minute' LIMIT 1000)`,
		`DELETE FROM worker_runtime_generations WHERE ctid IN(SELECT ctid FROM worker_runtime_generations WHERE deleted_at<now()-interval '24 hours' LIMIT 1000)`,
	} {
		if _, err := s.pool.Exec(ctx, query); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) RecordRuntimeResourceAcks(ctx context.Context, pool, pod, epoch, session string, refs []domain.ResourceRef) error {
	if len(refs) > 256 || pod == "" || len(pod) > 128 {
		return domain.Invalid("ACK_INVALID", "Invalid resource ACK")
	}
	deployments := make([]domain.ResourceRef, 0, len(refs))
	for _, ref := range refs {
		if ref.Kind == "deployment" {
			deployments = append(deployments, ref)
		}
	}
	refs = deployments
	// Immutable descriptor ACKs are connection-local. Persist only targets
	// needed by propagation queries, avoiding a ledger row per old Version.
	if len(refs) == 0 {
		return nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var current string
	if err = tx.QueryRow(ctx, `SELECT value FROM krun_meta WHERE key='worker_config_epoch' FOR SHARE`).Scan(&current); err != nil {
		return err
	}
	if epoch != current {
		return domain.Invalid("CONFIG_EPOCH_CHANGED", "Runtime configuration epoch changed")
	}
	kinds, ids, incarnations := []string{}, []string{}, []string{}
	generations := []int64{}
	seen := map[string]bool{}
	for _, r := range refs {
		if r.Generation < 1 || r.Incarnation == "" {
			return domain.Invalid("ACK_INVALID", "Invalid resource version")
		}
		if seen[r.Key()] {
			continue
		}
		seen[r.Key()] = true
		kinds = append(kinds, r.Kind)
		ids = append(ids, r.ID)
		incarnations = append(incarnations, r.Incarnation)
		generations = append(generations, r.Generation)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO worker_runtime_resource_acks(pod_uid,pool,config_epoch,kind,resource_id,incarnation,generation,session_id)
        SELECT $1,$2,$3,r.kind,r.id,r.incarnation,r.generation,$8 FROM unnest($4::text[],$5::text[],$6::text[],$7::bigint[]) r(kind,id,incarnation,generation)
        WHERE EXISTS(SELECT 1 FROM worker_runtime_sessions WHERE pod_uid=$1 AND session_id=$8 AND config_epoch=$3 AND connected)
        ON CONFLICT(pod_uid,kind,resource_id) DO UPDATE SET pool=excluded.pool,config_epoch=excluded.config_epoch,
        incarnation=excluded.incarnation,generation=excluded.generation,updated_at=now(),session_id=excluded.session_id
        WHERE (worker_runtime_resource_acks.pool,worker_runtime_resource_acks.config_epoch,worker_runtime_resource_acks.incarnation,worker_runtime_resource_acks.generation,worker_runtime_resource_acks.session_id)
        IS DISTINCT FROM (excluded.pool,excluded.config_epoch,excluded.incarnation,excluded.generation,excluded.session_id)`, pod, pool, epoch, kinds, ids, incarnations, generations, session); err != nil {
		return err
	}

	return tx.Commit(ctx)
}
