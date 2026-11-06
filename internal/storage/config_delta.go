package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/krun-dev/krun/internal/domain"
)

type WorkerConfigPriorityKey struct{}

type DirtyWorker struct {
	ID         string
	Generation int64
}

func (s *Store) DirtyWorkers(ctx context.Context) ([]DirtyWorker, error) {
	priority, _ := ctx.Value(WorkerConfigPriorityKey{}).(string)
	rows, err := s.pool.Query(ctx, `SELECT worker_id,generation FROM worker_config_dirty WHERE worker_id=$1 UNION ALL (SELECT worker_id,generation FROM worker_config_dirty WHERE worker_id<>$1 ORDER BY generation LIMIT 255)`, priority)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DirtyWorker{}
	for rows.Next() {
		var v DirtyWorker
		if err = rows.Scan(&v.ID, &v.Generation); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) PublishedWorkers(ctx context.Context, ids []string) (domain.WorkerConfigProjection, error) {
	out := domain.WorkerConfigProjection{Routes: []domain.WorkerConfigRoute{}, Versions: map[string]domain.WorkerVersion{}}
	rows, err := s.pool.Query(ctx, `SELECT worker_id,host,pool,path,enabled,version_id,expires_at FROM worker_config_routes WHERE worker_id=ANY($1) ORDER BY host`, ids)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var r domain.WorkerConfigRoute
		if err = rows.Scan(&r.Worker, &r.Host, &r.Pool, &r.Path, &r.Enabled, &r.Version, &r.Expires); err != nil {
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
	rows, err = s.pool.Query(ctx, `SELECT DISTINCT c.version_id,c.payload FROM worker_config_versions c JOIN worker_config_routes r ON r.version_id=c.version_id AND r.pool=c.pool WHERE r.worker_id=ANY($1)`, ids)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var raw []byte
		var rel domain.WorkerVersion
		if err = rows.Scan(&id, &raw); err != nil {
			return out, err
		}
		if err = json.Unmarshal(raw, &rel); err != nil {
			return out, err
		}
		out.Versions[id] = rel
	}
	return out, rows.Err()
}

func (s *Store) AppendConfigDelta(ctx context.Context, expected int64, dirty []DirtyWorker, ops []domain.WorkerConfigOp) (int64, bool, error) {
	ids := make([]string, 0, len(dirty))
	generations := make([]int64, 0, len(dirty))
	for _, d := range dirty {
		ids = append(ids, d.ID)
		generations = append(generations, d.Generation)
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
	if len(ops) == 0 {
		if err = acknowledgeWorkerConfig(ctx, tx, ids, generations); err != nil {
			return expected, false, err
		}
		var head int64
		if err = tx.QueryRow(ctx, `SELECT value::bigint FROM krun_meta WHERE key='worker_config_head'`).Scan(&head); err != nil {
			return expected, false, fmt.Errorf("read config head: %w", err)
		}
		if err = tx.Commit(ctx); err != nil {
			return expected, false, err
		}
		return head, true, nil
	}

	revision := expected + 1
	// Claim the head only if every worker still has the generation we read.
	// The head row serializes publishers until commit; worker mutations can
	// continue and their newer dirty generations must survive acknowledgment.
	tag, err := tx.Exec(ctx, `
		UPDATE krun_meta SET value=$2
		WHERE key='worker_config_head' AND value=$1
		  AND NOT EXISTS (
			SELECT 1 FROM unnest($3::text[], $4::bigint[]) AS incoming(worker_id, generation)
			WHERE NOT EXISTS (
				SELECT 1 FROM worker_config_dirty d
				WHERE d.worker_id=incoming.worker_id AND d.generation=incoming.generation
			)
		  )`, strconv.FormatInt(expected, 10), strconv.FormatInt(revision, 10), ids, generations)
	if err != nil {
		return expected, false, fmt.Errorf("claim config revision: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return expected, false, nil
	}
	// Read the old locations before changing routes. Moving a host to another
	// pool must invalidate both pools and prune the version left behind.
	shards, prune, err := affectedWorkerConfig(ctx, tx, ops)
	if err != nil {
		return expected, false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO worker_config_revisions(config_revision, digest) VALUES ($1, $2)`, revision, "delta:"+bytesDigest(raw)); err != nil {
		return expected, false, fmt.Errorf("insert config revision: %w", err)
	}
	if err = applyWorkerConfigOps(ctx, tx, revision, ops); err != nil {
		return expected, false, err
	}
	if err = writeWorkerConfigCheckpoints(ctx, tx, revision, shards); err != nil {
		return expected, false, err
	}
	if err = acknowledgeWorkerConfig(ctx, tx, ids, generations); err != nil {
		return expected, false, err
	}
	if err = pruneWorkerConfig(ctx, tx, revision, prune); err != nil {
		return expected, false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return expected, false, err
	}
	return revision, true, nil
}

func (s *Store) WorkerConfigShard(ctx context.Context, pool, shard string) (domain.WorkerConfigProjection, int64, error) {
	out := domain.WorkerConfigProjection{Routes: []domain.WorkerConfigRoute{}, Versions: map[string]domain.WorkerVersion{}}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return out, 0, err
	}
	defer tx.Rollback(ctx)
	var revision int64
	if err = tx.QueryRow(ctx, `SELECT value::bigint FROM krun_meta WHERE key='worker_config_head'`).Scan(&revision); err != nil {
		return out, 0, err
	}
	rows, err := tx.Query(ctx, `SELECT r.worker_id,r.host,r.pool,r.path,r.enabled,r.version_id,r.expires_at,c.payload FROM worker_config_routes r LEFT JOIN worker_config_versions c ON c.version_id=r.version_id AND c.pool=r.pool WHERE r.pool=$1 AND r.shard=$2 ORDER BY r.host`, pool, shard)
	if err != nil {
		return out, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var r domain.WorkerConfigRoute
		var raw []byte
		if err = rows.Scan(&r.Worker, &r.Host, &r.Pool, &r.Path, &r.Enabled, &r.Version, &r.Expires, &raw); err != nil {
			return out, 0, err
		}
		out.Routes = append(out.Routes, r)
		if len(raw) > 0 {
			var rel domain.WorkerVersion
			if err = json.Unmarshal(raw, &rel); err != nil {
				return out, 0, err
			}
			out.Versions[rel.ID] = rel
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

// Infrastructure catalogue changes can affect every hostname. They enqueue a
// one-time rebuild, then the normal publisher still processes bounded batches.
func (s *Store) EnsureWorkerConfigCatalogue(ctx context.Context, digest string) error {
	if s.pool == nil {
		return nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `INSERT INTO krun_meta(key,value) VALUES('worker_config_catalogue',$1) ON CONFLICT(key) DO UPDATE SET value=excluded.value WHERE krun_meta.value IS DISTINCT FROM excluded.value`, digest)
	if err != nil {
		return err
	}
	if tag.RowsAffected() > 0 {
		if _, err = tx.Exec(ctx, `INSERT INTO worker_config_dirty(worker_id) SELECT worker_id FROM krun_workers ON CONFLICT(worker_id) DO UPDATE SET generation=nextval('worker_dirty_seq')`); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
