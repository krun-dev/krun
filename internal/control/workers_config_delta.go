package control

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/minio/minio-go/v7"
)

type workerProjectionScopeKey struct{}
type workerConfigPriorityKey struct{}

func queryWorkerProjection(ctx context.Context, tx pgx.Tx, query string) (pgx.Rows, error) {
	ids, ok := ctx.Value(workerProjectionScopeKey{}).([]string)
	if !ok {
		return tx.Query(ctx, query)
	}
	parts := strings.SplitN(query, "ORDER BY", 2)
	query = parts[0] + " WHERE s.service_id=ANY($1) "
	if len(parts) > 1 {
		query += "ORDER BY" + parts[1]
	}
	return tx.Query(ctx, query, ids)
}

type workerDirtyService struct {
	ID         string
	Generation int64
}

func (s *Store) dirtyWorkerServices(ctx context.Context) ([]workerDirtyService, error) {
	priority, _ := ctx.Value(workerConfigPriorityKey{}).(string)
	rows, err := s.pool.Query(ctx, `SELECT service_id,generation FROM worker_config_dirty WHERE service_id=$1 UNION ALL (SELECT service_id,generation FROM worker_config_dirty WHERE service_id<>$1 ORDER BY generation LIMIT 255)`, priority)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []workerDirtyService{}
	for rows.Next() {
		var v workerDirtyService
		if err = rows.Scan(&v.ID, &v.Generation); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) publishedWorkerServices(ctx context.Context, ids []string) (workerConfigProjection, error) {
	out := workerConfigProjection{Routes: []workerConfigRoute{}, Releases: map[string]WorkerRelease{}}
	rows, err := s.pool.Query(ctx, `SELECT service_id,host,pool,path,enabled,release_id FROM worker_config_routes WHERE service_id=ANY($1) ORDER BY host`, ids)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var r workerConfigRoute
		if err = rows.Scan(&r.Service, &r.Host, &r.Pool, &r.Path, &r.Enabled, &r.Release); err != nil {
			rows.Close()
			return out, err
		}
		out.Routes = append(out.Routes, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	rows, err = s.pool.Query(ctx, `SELECT DISTINCT c.release_id,c.payload FROM worker_config_releases c JOIN worker_config_routes r ON r.release_id=c.release_id AND r.pool=c.pool WHERE r.service_id=ANY($1)`, ids)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var raw []byte
		var rel WorkerRelease
		if err = rows.Scan(&id, &raw); err != nil {
			return out, err
		}
		if err = json.Unmarshal(raw, &rel); err != nil {
			return out, err
		}
		out.Releases[id] = rel
	}
	return out, rows.Err()
}

// Only the dirty service batch is read and diffed. Queue generations protect a
// newer mutation from being acknowledged by an older publisher. The revision
// transaction contains database statements only; all S3 writes are retryable.
func (w *workersController) publishConfigDelta(ctx context.Context) (int64, error) {
	for attempt := 0; attempt < 8; attempt++ {
		head, _, err := w.server.Store.ConfigState(ctx)
		if err != nil {
			return 0, err
		}
		dirty, err := w.server.Store.dirtyWorkerServices(ctx)
		if err != nil {
			return head, err
		}
		if len(dirty) == 0 {
			return head, w.writePendingCheckpoints(ctx, head)
		}
		ids := make([]string, 0, len(dirty))
		for _, d := range dirty {
			ids = append(ids, d.ID)
		}
		next, _, err := w.server.Store.WorkerConfigProjection(context.WithValue(ctx, workerProjectionScopeKey{}, ids), w.config)
		if err != nil {
			return head, err
		}
		old, err := w.server.Store.publishedWorkerServices(ctx, ids)
		if err != nil {
			return head, err
		}
		ops := diffWorkerConfig(&old, next)
		revision, won, err := w.server.Store.appendConfigDelta(ctx, head, dirty, ops)
		if err != nil {
			return head, err
		}
		if !won {
			continue
		}
		w.server.Store.SignalConfig()
		return revision, w.writePendingCheckpoints(ctx, revision)
	}
	return 0, errors.New("edge config publisher CAS contention")
}
func (s *Store) appendConfigDelta(ctx context.Context, expected int64, dirty []workerDirtyService, ops []workerConfigOp) (int64, bool, error) {
	ids := make([]string, 0, len(dirty))
	generations := make([]int64, 0, len(dirty))
	for _, d := range dirty {
		ids = append(ids, d.ID)
		generations = append(generations, d.Generation)
	}
	if len(ops) == 0 {
		var head int64
		err := s.pool.QueryRow(ctx, `WITH acknowledged AS(DELETE FROM worker_config_dirty d USING unnest($1::text[],$2::bigint[]) incoming(service_id,generation) WHERE d.service_id=incoming.service_id AND d.generation=incoming.generation) SELECT value::bigint FROM krun_meta WHERE key='worker_config_head'`, ids, generations).Scan(&head)
		return head, true, err
	}
	raw, err := json.Marshal(ops)
	if err != nil {
		return expected, false, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return expected, false, err
	}
	defer tx.Rollback(ctx)
	revision := expected + 1
	// One conditional statement validates the entire read generation and acquires
	// the head. The following three statements are bounded by this delta batch.
	tag, err := tx.Exec(ctx, `UPDATE krun_meta SET value=$2 WHERE key='worker_config_head' AND value=$1 AND NOT EXISTS(
 SELECT 1 FROM unnest($3::text[],$4::bigint[]) incoming(service_id,generation)
 WHERE NOT EXISTS(SELECT 1 FROM worker_config_dirty d WHERE d.service_id=incoming.service_id AND d.generation=incoming.generation))`, strconv.FormatInt(expected, 10), strconv.FormatInt(revision, 10), ids, generations)
	if err != nil {
		return expected, false, err
	}
	if tag.RowsAffected() != 1 {
		return expected, false, nil
	}
	var prune []string
	err = tx.QueryRow(ctx, `WITH incoming AS MATERIALIZED (
 SELECT item.value,item.ordinality-1 AS ordinal,item.value->>'type' AS type,
 COALESCE(item.value->>'pool',item.value#>>'{route,pool}','default') AS pool,
 COALESCE(item.value->>'host','') AS host,
 COALESCE(item.value#>>'{release,id}',item.value#>>'{route,release}','') AS release_id
 FROM jsonb_array_elements($3::jsonb) WITH ORDINALITY item(value,ordinality)
 ), old_routes AS MATERIALIZED (
 SELECT r.* FROM worker_config_routes r JOIN incoming i ON i.host=r.host WHERE i.type IN('route_upsert','route_delete')
 ), inserted_revision AS(
 INSERT INTO worker_config_revisions(config_revision,digest) VALUES($1,$2)
 ), released AS(
 INSERT INTO worker_config_releases(release_id,pool,payload)
 SELECT release_id,pool,value->'release' FROM incoming WHERE type='release_put'
 ON CONFLICT(release_id,pool) DO UPDATE SET payload=excluded.payload WHERE worker_config_releases.payload IS DISTINCT FROM excluded.payload
 ), routed AS(
 INSERT INTO worker_config_routes(host,pool,path,enabled,release_id,service_id,shard)
 SELECT host,pool,COALESCE(value#>>'{route,path}',''),COALESCE((value#>>'{route,enabled}')::boolean,false),release_id,COALESCE(value#>>'{route,service_id}',''),left(encode(sha256(convert_to(host,'UTF8')),'hex'),2)
 FROM incoming WHERE type='route_upsert'
 ON CONFLICT(host) DO UPDATE SET pool=excluded.pool,path=excluded.path,enabled=excluded.enabled,release_id=excluded.release_id,service_id=excluded.service_id,shard=excluded.shard
 ), deleted AS(
 DELETE FROM worker_config_routes WHERE host IN(SELECT host FROM incoming WHERE type='route_delete')
 ), ops AS(
 INSERT INTO worker_config_ops(config_revision,ordinal,pool,op_type,host,release_id,payload)
 SELECT $1,ordinal::int,pool,type,host,release_id,CASE type WHEN 'release_put' THEN value->'release' WHEN 'route_upsert' THEN value->'route' END FROM incoming
 ), checkpoints AS(
 INSERT INTO worker_config_checkpoints(pool,shard,revision)
 SELECT pool,shard,$1 FROM(SELECT pool,left(encode(sha256(convert_to(host,'UTF8')),'hex'),2) AS shard FROM incoming WHERE host<>'' UNION SELECT pool,shard FROM old_routes) affected
 ON CONFLICT(pool,shard) DO UPDATE SET revision=excluded.revision
 ), acknowledged AS(
 DELETE FROM worker_config_dirty d USING unnest($4::text[],$5::bigint[]) incoming(service_id,generation)
 WHERE d.service_id=incoming.service_id AND d.generation=incoming.generation
 ) SELECT COALESCE(array_agg(DISTINCT release_id) FILTER(WHERE release_id<>''),'{}'::text[]) FROM(SELECT release_id FROM old_routes UNION SELECT release_id FROM incoming) candidates`, revision, "delta:"+bytesDigest(raw), raw, ids, generations).Scan(&prune)
	if err != nil {
		return expected, false, err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM worker_config_releases c WHERE release_id=ANY($1) AND NOT EXISTS(SELECT 1 FROM worker_config_routes r WHERE r.release_id=c.release_id AND r.pool=c.pool)`, prune); err != nil {
		return expected, false, err
	}
	floor := revision - workerConfigOpsRetain + 1
	if _, err = tx.Exec(ctx, `WITH deleted AS(DELETE FROM worker_config_ops WHERE config_revision<$1) DELETE FROM worker_config_revisions WHERE config_revision<$1`, floor); err != nil {
		return expected, false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return expected, false, err
	}
	return revision, true, nil
}

func (s *Store) workerConfigShard(ctx context.Context, pool, shard string) (workerConfigProjection, int64, error) {
	out := workerConfigProjection{Routes: []workerConfigRoute{}, Releases: map[string]WorkerRelease{}}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return out, 0, err
	}
	defer tx.Rollback(ctx)
	var revision int64
	if err = tx.QueryRow(ctx, `SELECT value::bigint FROM krun_meta WHERE key='worker_config_head'`).Scan(&revision); err != nil {
		return out, 0, err
	}
	rows, err := tx.Query(ctx, `SELECT r.service_id,r.host,r.pool,r.path,r.enabled,r.release_id,c.payload FROM worker_config_routes r LEFT JOIN worker_config_releases c ON c.release_id=r.release_id AND c.pool=r.pool WHERE r.pool=$1 AND r.shard=$2 ORDER BY r.host`, pool, shard)
	if err != nil {
		return out, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var r workerConfigRoute
		var raw []byte
		if err = rows.Scan(&r.Service, &r.Host, &r.Pool, &r.Path, &r.Enabled, &r.Release, &raw); err != nil {
			return out, 0, err
		}
		out.Routes = append(out.Routes, r)
		if len(raw) > 0 {
			var rel WorkerRelease
			if err = json.Unmarshal(raw, &rel); err != nil {
				return out, 0, err
			}
			out.Releases[rel.ID] = rel
		}
	}
	if err = rows.Err(); err != nil {
		return out, 0, err
	}
	rows.Close()
	if err = tx.Commit(ctx); err != nil {
		return out, 0, err
	}
	return out, revision, nil
}
func (w *workersController) writePendingCheckpoints(ctx context.Context, head int64) error {
	if w.s3 == nil && w.putObject == nil {
		return nil
	}
	if w.s3 != nil && !w.configBucket {
		if err := w.ensureBucket(ctx); err != nil {
			return err
		}
		w.configBucket = true
	}
	rows, err := w.server.Store.pool.Query(ctx, `SELECT pool,shard,revision FROM worker_config_checkpoints WHERE published_revision<revision ORDER BY revision,pool,shard LIMIT 32`)
	if err != nil {
		return err
	}
	type checkpoint struct {
		pool, shard string
		revision    int64
	}
	pending := []checkpoint{}
	for rows.Next() {
		var p checkpoint
		if err = rows.Scan(&p.pool, &p.shard, &p.revision); err != nil {
			rows.Close()
			return err
		}
		pending = append(pending, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, p := range pending {
		projection, snapshotRevision, e := w.server.Store.workerConfigShard(ctx, p.pool, p.shard)
		if e != nil {
			return e
		}
		p.revision = snapshotRevision
		raw, e := encodeWorkerConfigSnapshot(p.pool, p.revision, projection)
		if e != nil {
			return e
		}
		relative := "shards/" + p.shard + "/" + strconv.FormatInt(p.revision, 10) + ".json.gz"
		key := workerPoolConfigPrefix(p.pool) + relative
		if w.s3 != nil {
			e = w.putImmutable(ctx, key, raw)
		} else {
			e = w.putBytes(ctx, key, raw, minio.PutObjectOptions{ContentType: "application/gzip"})
		}
		if e != nil {
			return e
		}
		latest := workerConfigLatest{ConfigRevision: p.revision, Snapshot: relative, SHA256: bytesDigest(raw)}
		if e = w.writeConfigLatest(ctx, workerPoolConfigPrefix(p.pool)+"shards/"+p.shard+"/latest.json", latest); e != nil {
			return e
		}
		if _, e = w.server.Store.pool.Exec(ctx, `UPDATE worker_config_checkpoints SET published_revision=GREATEST(published_revision,$3) WHERE pool=$1 AND shard=$2`, p.pool, p.shard, p.revision); e != nil {
			return e
		}
	}
	pools := append([]WorkerRuntimePool(nil), w.config.configuredRuntimePools()...)
	sort.Slice(pools, func(i, j int) bool { return pools[i].ID < pools[j].ID })
	for _, p := range pools {
		if err = w.writeConfigLatest(ctx, workerPoolConfigLatestKey(p.ID), workerConfigLatest{LazyRoutes: true, ConfigRevision: head, OpsFloor: max(1, head-workerConfigOpsRetain+1)}); err != nil {
			return err
		}
	}
	return nil
}

// Infrastructure catalogue changes can affect every hostname. They enqueue a
// one-time rebuild, then the normal publisher still processes bounded batches.
func (s *Store) ensureWorkerConfigCatalogue(ctx context.Context, config *WorkersConfig) error {
	if s.pool == nil {
		return nil
	}
	raw, err := json.Marshal(struct {
		Shared   bool
		Pools    []WorkerRuntimePool
		Domains  []WorkerDomain
		Profiles []WorkerNetworkProfile
	}{config.SharedRouting, config.RuntimePools, config.Domains, config.NetworkProfiles})
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `INSERT INTO krun_meta(key,value) VALUES('worker_config_catalogue',$1) ON CONFLICT(key) DO UPDATE SET value=excluded.value WHERE krun_meta.value IS DISTINCT FROM excluded.value`, bytesDigest(raw))
	if err != nil {
		return err
	}
	if tag.RowsAffected() > 0 {
		// Recover service identities of existing pre-release host rows once. The
		// stable slug hash permits a hash join, including unpublished environments.
		if _, err = tx.Exec(ctx, `UPDATE worker_config_routes r SET service_id=s.service_id,shard=left(encode(sha256(convert_to(r.host,'UTF8')),'hex'),2) FROM krun_worker_services s WHERE r.service_id='' AND left(r.host,18)='w-'||left(encode(sha256(convert_to(s.org_id||'/'||s.service_id,'UTF8')),'hex'),16)`); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO worker_config_dirty(service_id) SELECT service_id FROM krun_worker_services ON CONFLICT(service_id) DO UPDATE SET generation=nextval('worker_dirty_seq')`); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
