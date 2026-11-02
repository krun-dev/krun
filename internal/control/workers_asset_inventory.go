package control

import (
	"context"
	"errors"
	"strings"

	"github.com/minio/minio-go/v7"
)

func workerArtifactManifestKey(a WorkerArtifact) string {
	return strings.TrimSuffix(a.ObjectKey, "bundle.tar.gz") + "manifest.json"
}
func (s *Store) registerAssetObjects(ctx context.Context, artifact string, keys []string) error {
	if s.pool == nil || len(keys) == 0 {
		return nil
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO worker_asset_objects(object_key,artifact_id) SELECT unnest($1::text[]),$2 ON CONFLICT(object_key) DO NOTHING`, keys, artifact)
	return err
}
func (w *workersController) collectArtifactAssets(ctx context.Context, a WorkerArtifact) (bool, error) {
	if w.server.Store.pool == nil {
		return true, nil
	}
	rows, err := w.server.Store.pool.Query(ctx, `SELECT object_key FROM worker_asset_objects WHERE artifact_id=$1 ORDER BY object_key LIMIT 1000`, a.ID)
	if err != nil {
		return false, err
	}
	keys := []string{}
	for rows.Next() {
		var key string
		if err = rows.Scan(&key); err != nil {
			rows.Close()
			return false, err
		}
		keys = append(keys, key)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return false, err
	}
	// One S3 multi-delete and one indexed metadata statement per batch. On a
	// partial failure keep the inventory so the next retry is idempotent.
	if len(keys) > 0 {
		objects := make(chan minio.ObjectInfo, len(keys))
		for _, key := range keys {
			objects <- minio.ObjectInfo{Key: key}
		}
		close(objects)
		var failures []error
		for result := range w.s3.RemoveObjects(ctx, a.Bucket, objects, minio.RemoveObjectsOptions{}) {
			if result.Err != nil {
				failures = append(failures, result.Err)
			}
		}
		if err = errors.Join(failures...); err != nil {
			return false, err
		}
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		if _, err = w.server.Store.pool.Exec(ctx, `DELETE FROM worker_asset_objects WHERE artifact_id=$1 AND object_key=ANY($2)`, a.ID, keys); err != nil {
			return false, err
		}
	}

	if len(keys) == 1000 {
		_, err = w.server.Store.pool.Exec(ctx, `UPDATE krun_worker_artifacts SET gc_claimed_at=NULL WHERE artifact_id=$1 AND state='deleting' AND gc_claimed_by=$2`, a.ID, w.replica)
		return false, err
	}
	return true, nil
}
