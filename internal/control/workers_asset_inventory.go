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

func (w *workersController) collectArtifactAssets(ctx context.Context, a WorkerArtifact) (bool, error) {
	if !w.server.Store.Persistent() {
		return true, nil
	}
	keys, err := w.server.Store.ArtifactAssetObjects(ctx, a.ID, 1000)
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
		if err = w.server.Store.DeleteArtifactAssetObjects(ctx, a.ID, keys); err != nil {
			return false, err
		}
	}

	if len(keys) == 1000 {
		err = w.server.Store.ReleaseArtifactGCClaim(ctx, a.ID, w.replica)
		return false, err
	}
	return true, nil
}
