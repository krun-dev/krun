package storage

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/krun-dev/krun/internal/domain"
)

type ConfigCheckpoint struct {
	Pool              string
	Shard             string
	Revision          int64
	PublishedRevision int64
}

func (s *Store) PendingConfigCheckpoints(ctx context.Context, limit int) ([]ConfigCheckpoint, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT pool, shard, revision, published_revision
		FROM worker_config_checkpoints
		WHERE published_revision<revision ORDER BY revision, pool, shard LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return pgx.CollectRows(rows, pgx.RowToStructByPos[ConfigCheckpoint])
}

func (s *Store) PublishedConfigCheckpoints(ctx context.Context) ([]ConfigCheckpoint, error) {
	if s.pool == nil {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT pool, shard, revision, published_revision
		FROM worker_config_checkpoints WHERE published_revision>0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return pgx.CollectRows(rows, pgx.RowToStructByPos[ConfigCheckpoint])
}

func (s *Store) MarkConfigCheckpointPublished(ctx context.Context, pool, shard string, revision int64) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE worker_config_checkpoints SET published_revision=GREATEST(published_revision, $3)
		WHERE pool=$1 AND shard=$2`, pool, shard, revision)
	return err
}

func (s *Store) RuntimeConfigRoute(ctx context.Context, host, pool string) (domain.WorkerConfigProjection, int64, error) {
	out := domain.WorkerConfigProjection{Versions: map[string]domain.WorkerVersion{}}
	if s.pool == nil {
		return out, 0, domain.NotFound()
	}
	var route domain.WorkerConfigRoute
	var payload []byte
	var revision int64
	err := s.pool.QueryRow(ctx, `
		SELECT r.worker_id, r.host, r.pool, r.path, r.enabled, r.version_id, r.expires_at, c.payload,
			COALESCE((SELECT value::bigint FROM krun_meta WHERE key='worker_config_head'), 0)
		FROM worker_config_routes r
		LEFT JOIN worker_config_versions c ON c.version_id=r.version_id AND c.pool=r.pool
		WHERE r.host=$1 AND r.pool=$2`, host, pool).Scan(
		&route.Worker, &route.Host, &route.Pool, &route.Path, &route.Enabled, &route.Version, &route.Expires, &payload, &revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, 0, domain.NotFound()
	}
	if err != nil {
		return out, 0, err
	}
	out.Routes = []domain.WorkerConfigRoute{route}
	if len(payload) > 0 {
		var version domain.WorkerVersion
		if err := json.Unmarshal(payload, &version); err != nil {
			return out, 0, err
		}
		out.Versions[version.ID] = version
	}
	return out, revision, nil
}
