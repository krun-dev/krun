package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/krun-dev/krun/internal/domain"
)

type workerConfigShardKey struct {
	pool  string
	shard string
}

func affectedWorkerConfig(ctx context.Context, tx pgx.Tx, ops []domain.WorkerConfigOp) ([]workerConfigShardKey, []string, error) {
	hosts := []string{}
	shards := map[workerConfigShardKey]bool{}
	versions := map[string]bool{}
	for _, op := range ops {
		if op.Type == "route_upsert" || op.Type == "route_delete" {
			hosts = append(hosts, op.Host)
			shards[workerConfigShardKey{op.Pool, bytesDigest([]byte(op.Host))[:2]}] = true
		}
		if op.Version != nil && op.Version.ID != "" {
			versions[op.Version.ID] = true
		}
		if op.Route != nil && op.Route.Version != "" {
			versions[op.Route.Version] = true
		}
	}
	rows, err := tx.Query(ctx, `
		SELECT pool, shard, version_id FROM worker_config_routes
		WHERE host=ANY($1)`, hosts)
	if err != nil {
		return nil, nil, fmt.Errorf("read affected config routes: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var key workerConfigShardKey
		var version string
		if err = rows.Scan(&key.pool, &key.shard, &version); err != nil {
			return nil, nil, fmt.Errorf("read affected config route: %w", err)
		}
		shards[key] = true
		if version != "" {
			versions[version] = true
		}
	}
	if err = rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("read affected config routes: %w", err)
	}
	keys := make([]workerConfigShardKey, 0, len(shards))
	for key := range shards {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].pool != keys[j].pool {
			return keys[i].pool < keys[j].pool
		}
		return keys[i].shard < keys[j].shard
	})
	prune := make([]string, 0, len(versions))
	for id := range versions {
		prune = append(prune, id)
	}
	sort.Strings(prune)
	return keys, prune, nil
}

// Batch saves round trips; these are ordinary statements executed in order
// inside the caller's transaction. An error still rolls back the entire delta.
func applyWorkerConfigOps(ctx context.Context, tx pgx.Tx, revision int64, ops []domain.WorkerConfigOp) error {
	batch := &pgx.Batch{}
	for ordinal, op := range ops {
		var versionID string
		var payload []byte
		var err error
		switch op.Type {
		case "version_put":
			if op.Version == nil {
				return fmt.Errorf("config op %d: version_put requires a version", ordinal)
			}
			versionID = op.Version.ID
			payload, err = json.Marshal(op.Version)
			batch.Queue(`
				INSERT INTO worker_config_versions(version_id, pool, payload)
				VALUES ($1, $2, $3)
				ON CONFLICT (version_id, pool) DO UPDATE SET payload=excluded.payload
				WHERE worker_config_versions.payload IS DISTINCT FROM excluded.payload`,
				versionID, op.Pool, payload)
		case "route_upsert":
			if op.Route == nil {
				return fmt.Errorf("config op %d: route_upsert requires a route", ordinal)
			}
			route := op.Route
			versionID = route.Version
			payload, err = json.Marshal(route)
			batch.Queue(`
				INSERT INTO worker_config_routes(host, pool, path, enabled, version_id, worker_id, shard, expires_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
				ON CONFLICT (host) DO UPDATE SET
					pool=excluded.pool, path=excluded.path, enabled=excluded.enabled,
					version_id=excluded.version_id, worker_id=excluded.worker_id, shard=excluded.shard, expires_at=excluded.expires_at`,
				op.Host, op.Pool, route.Path, route.Enabled, versionID, route.Worker, bytesDigest([]byte(op.Host))[:2], route.Expires)
		case "route_delete":
			batch.Queue(`DELETE FROM worker_config_routes WHERE host=$1`, op.Host)
		default:
			return fmt.Errorf("config op %d: unknown type %q", ordinal, op.Type)
		}
		if err != nil {
			return fmt.Errorf("encode config op %d: %w", ordinal, err)
		}
		batch.Queue(`
			INSERT INTO worker_config_ops(config_revision, ordinal, pool, op_type, host, version_id, payload)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			revision, ordinal, op.Pool, op.Type, op.Host, versionID, payload)
	}
	if err := tx.SendBatch(ctx, batch).Close(); err != nil {
		return fmt.Errorf("apply config operations: %w", err)
	}
	return nil
}

func writeWorkerConfigCheckpoints(ctx context.Context, tx pgx.Tx, revision int64, shards []workerConfigShardKey) error {
	batch := &pgx.Batch{}
	for _, key := range shards {
		batch.Queue(`
			INSERT INTO worker_config_checkpoints(pool, shard, revision) VALUES ($1, $2, $3)
			ON CONFLICT (pool, shard) DO UPDATE SET revision=excluded.revision`, key.pool, key.shard, revision)
	}
	if err := tx.SendBatch(ctx, batch).Close(); err != nil {
		return fmt.Errorf("update config checkpoints: %w", err)
	}
	return nil
}

func acknowledgeWorkerConfig(ctx context.Context, tx pgx.Tx, ids []string, generations []int64) error {
	_, err := tx.Exec(ctx, `
		DELETE FROM worker_config_dirty d
		USING unnest($1::text[], $2::bigint[]) AS incoming(worker_id, generation)
		WHERE d.worker_id=incoming.worker_id AND d.generation=incoming.generation`, ids, generations)
	if err != nil {
		return fmt.Errorf("acknowledge config dirty generations: %w", err)
	}
	return nil
}

func pruneWorkerConfig(ctx context.Context, tx pgx.Tx, revision int64, versions []string) error {
	if _, err := tx.Exec(ctx, `
		DELETE FROM worker_config_versions c
		WHERE version_id=ANY($1) AND NOT EXISTS (
			SELECT 1 FROM worker_config_routes r WHERE r.version_id=c.version_id AND r.pool=c.pool
		)`, versions); err != nil {
		return fmt.Errorf("prune unreferenced config versions: %w", err)
	}
	floor := revision - WorkerConfigOpsRetain + 1
	if _, err := tx.Exec(ctx, `DELETE FROM worker_config_ops WHERE config_revision<$1`, floor); err != nil {
		return fmt.Errorf("prune config operations: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM worker_config_revisions WHERE config_revision<$1`, floor); err != nil {
		return fmt.Errorf("prune config revisions: %w", err)
	}
	return nil
}
