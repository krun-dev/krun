package control

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"
)

type workerConfigShardKey struct {
	pool  string
	shard string
}

func affectedWorkerConfig(ctx context.Context, tx pgx.Tx, ops []workerConfigOp) ([]workerConfigShardKey, []string, error) {
	hosts := []string{}
	shards := map[workerConfigShardKey]bool{}
	releases := map[string]bool{}
	for _, op := range ops {
		if op.Type == "route_upsert" || op.Type == "route_delete" {
			hosts = append(hosts, op.Host)
			shards[workerConfigShardKey{op.Pool, bytesDigest([]byte(op.Host))[:2]}] = true
		}
		if op.Release != nil && op.Release.ID != "" {
			releases[op.Release.ID] = true
		}
		if op.Route != nil && op.Route.Release != "" {
			releases[op.Route.Release] = true
		}
	}
	rows, err := tx.Query(ctx, `
		SELECT pool, shard, release_id FROM worker_config_routes
		WHERE host=ANY($1)`, hosts)
	if err != nil {
		return nil, nil, fmt.Errorf("read affected config routes: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var key workerConfigShardKey
		var release string
		if err = rows.Scan(&key.pool, &key.shard, &release); err != nil {
			return nil, nil, fmt.Errorf("read affected config route: %w", err)
		}
		shards[key] = true
		if release != "" {
			releases[release] = true
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
	prune := make([]string, 0, len(releases))
	for id := range releases {
		prune = append(prune, id)
	}
	sort.Strings(prune)
	return keys, prune, nil
}

// Batch saves round trips; these are ordinary statements executed in order
// inside the caller's transaction. An error still rolls back the entire delta.
func applyWorkerConfigOps(ctx context.Context, tx pgx.Tx, revision int64, ops []workerConfigOp) error {
	batch := &pgx.Batch{}
	for ordinal, op := range ops {
		var releaseID string
		var payload []byte
		var err error
		switch op.Type {
		case "release_put":
			if op.Release == nil {
				return fmt.Errorf("config op %d: release_put requires a release", ordinal)
			}
			releaseID = op.Release.ID
			payload, err = json.Marshal(op.Release)
			batch.Queue(`
				INSERT INTO worker_config_releases(release_id, pool, payload)
				VALUES ($1, $2, $3)
				ON CONFLICT (release_id, pool) DO UPDATE SET payload=excluded.payload
				WHERE worker_config_releases.payload IS DISTINCT FROM excluded.payload`,
				releaseID, op.Pool, payload)
		case "route_upsert":
			if op.Route == nil {
				return fmt.Errorf("config op %d: route_upsert requires a route", ordinal)
			}
			route := op.Route
			releaseID = route.Release
			payload, err = json.Marshal(route)
			batch.Queue(`
				INSERT INTO worker_config_routes(host, pool, path, enabled, release_id, service_id, shard)
				VALUES ($1, $2, $3, $4, $5, $6, $7)
				ON CONFLICT (host) DO UPDATE SET
					pool=excluded.pool, path=excluded.path, enabled=excluded.enabled,
					release_id=excluded.release_id, service_id=excluded.service_id, shard=excluded.shard`,
				op.Host, op.Pool, route.Path, route.Enabled, releaseID, route.Service, bytesDigest([]byte(op.Host))[:2])
		case "route_delete":
			batch.Queue(`DELETE FROM worker_config_routes WHERE host=$1`, op.Host)
		default:
			return fmt.Errorf("config op %d: unknown type %q", ordinal, op.Type)
		}
		if err != nil {
			return fmt.Errorf("encode config op %d: %w", ordinal, err)
		}
		batch.Queue(`
			INSERT INTO worker_config_ops(config_revision, ordinal, pool, op_type, host, release_id, payload)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			revision, ordinal, op.Pool, op.Type, op.Host, releaseID, payload)
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
		USING unnest($1::text[], $2::bigint[]) AS incoming(service_id, generation)
		WHERE d.service_id=incoming.service_id AND d.generation=incoming.generation`, ids, generations)
	if err != nil {
		return fmt.Errorf("acknowledge config dirty generations: %w", err)
	}
	return nil
}

func pruneWorkerConfig(ctx context.Context, tx pgx.Tx, revision int64, releases []string) error {
	if _, err := tx.Exec(ctx, `
		DELETE FROM worker_config_releases c
		WHERE release_id=ANY($1) AND NOT EXISTS (
			SELECT 1 FROM worker_config_routes r WHERE r.release_id=c.release_id AND r.pool=c.pool
		)`, releases); err != nil {
		return fmt.Errorf("prune unreferenced config releases: %w", err)
	}
	floor := revision - workerConfigOpsRetain + 1
	if _, err := tx.Exec(ctx, `DELETE FROM worker_config_ops WHERE config_revision<$1`, floor); err != nil {
		return fmt.Errorf("prune config operations: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM worker_config_revisions WHERE config_revision<$1`, floor); err != nil {
		return fmt.Errorf("prune config revisions: %w", err)
	}
	return nil
}
