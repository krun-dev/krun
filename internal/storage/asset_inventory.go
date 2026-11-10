package storage

import (
	"context"
)

func (s *Store) RegisterAssetObjects(ctx context.Context, artifact string, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO worker_asset_objects(object_key,artifact_id) SELECT unnest($1::text[]),$2 ON CONFLICT(object_key) DO NOTHING`, keys, artifact)
	return err
}

func (s *Store) ArtifactAssetObjects(ctx context.Context, artifact string, limit int) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT object_key FROM worker_asset_objects WHERE artifact_id=$1 ORDER BY object_key LIMIT $2`, artifact, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := []string{}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

func (s *Store) DeleteArtifactAssetObjects(ctx context.Context, artifact string, keys []string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM worker_asset_objects WHERE artifact_id=$1 AND object_key=ANY($2)`, artifact, keys)
	return err
}

func (s *Store) ReleaseArtifactGCClaim(ctx context.Context, artifact, replica string) error {
	_, err := s.pool.Exec(ctx, `UPDATE krun_worker_artifacts SET gc_claimed_at=NULL WHERE artifact_id=$1 AND state='deleting' AND gc_claimed_by=$2`, artifact, replica)
	return err
}
