package storage

import (
	"context"

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
	rows, err := s.pool.Query(ctx, `
		SELECT pool, shard, revision, published_revision
		FROM worker_config_checkpoints WHERE published_revision>0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return pgx.CollectRows(rows, pgx.RowToStructByPos[ConfigCheckpoint])
}

func (s *Store) MarkConfigCheckpointPublished(ctx context.Context, pool, shard string, position domain.EdgeConfigCursor) error {
	_, err := s.pool.Exec(ctx, `
		WITH epoch AS MATERIALIZED (
			SELECT value FROM krun_meta WHERE key='worker_config_epoch' FOR SHARE
		)
		UPDATE worker_config_checkpoints SET published_revision=GREATEST(published_revision, $3)
		FROM epoch WHERE pool=$1 AND shard=$2 AND epoch.value=$4`, pool, shard, position.Revision, position.Epoch)
	return err
}

func (s *Store) RuntimeConfigRoute(ctx context.Context, host, pool string) (domain.EdgeConfigProjection, domain.EdgeConfigCursor, error) {
	out := domain.EdgeConfigProjection{Versions: map[string]domain.WorkerVersion{}}

	var position domain.EdgeConfigCursor
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return out, position, err
	}
	defer tx.Rollback(ctx)
	if err = tx.QueryRow(ctx, readConfigPosition).Scan(&position.Epoch, &position.Revision); err != nil {
		return out, position, err
	}
	route, err := scanEdgeRoute(tx.QueryRow(ctx, "SELECT "+edgeRouteColumns+" FROM worker_config_routes r WHERE r.host=$1 AND r.pool=$2", host, pool))
	if err == pgx.ErrNoRows {
		return out, position, domain.NotFound()
	}
	if err != nil {
		return out, position, err
	}
	out.Routes = []domain.EdgeConfigRoute{route}
	if err = readEdgeRouteVersions(ctx, tx, pool, out.Routes, out.Versions); err != nil {
		return out, position, err
	}
	if err = tx.Commit(ctx); err != nil {
		return out, position, err
	}
	return out, position, nil
}

// PublishedAssetHosts identifies active routes for an immutable artifact in a
// single runtime pool. The caller resolves each route's configured visibility.
func (s *Store) PublishedAssetHosts(ctx context.Context, organization, worker, digest, pool string) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT r.host
		FROM worker_config_routes r
		JOIN krun_worker_versions v ON ((v.version_id=r.version_id AND r.candidate_weight<100000)
			OR (v.version_id=r.candidate_version_id AND r.candidate_weight>0)
			OR EXISTS (SELECT 1 FROM jsonb_array_elements(r.retained_versions) a WHERE a->>'version_id'=v.version_id AND (a->>'expires_at')::timestamptz > now())) AND v.worker_id = r.worker_id
		WHERE r.worker_id = $1 AND v.org_id = $2 AND v.archive_digest = $3 AND r.pool = $4
		  AND r.enabled AND (r.expires_at IS NULL OR r.expires_at > now())`,
		worker, organization, digest, pool)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}
