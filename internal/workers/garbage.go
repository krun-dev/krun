package workers

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"

	objectstore "github.com/krun-dev/krun/internal/adapters/objectstore"
	domain "github.com/krun-dev/krun/internal/domain"
	storage "github.com/krun-dev/krun/internal/storage"
)

func WorkerArtifactManifestKey(a domain.WorkerArtifact) string {
	return strings.TrimSuffix(a.ObjectKey, "bundle.tar.gz") + "manifest.json"
}

func (w *Controller) CollectArtifactAssets(ctx context.Context, a domain.WorkerArtifact) (bool, error) {
	if !w.Service.Store.Persistent() {
		return true, nil
	}
	keys, err := w.Service.Store.ArtifactAssetObjects(ctx, a.ID, 1000)
	if err != nil {
		return false, err
	}
	// One S3 multi-delete and one indexed metadata statement per batch. On a
	// partial failure keep the inventory so the next retry is idempotent.
	if len(keys) > 0 {
		objects := make(chan objectstore.ObjectInfo, len(keys))
		for _, key := range keys {
			objects <- objectstore.ObjectInfo{Key: key}
		}
		close(objects)
		var failures []error
		for result := range w.S3.RemoveObjects(ctx, a.Bucket, objects, objectstore.RemoveObjectsOptions{}) {
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
		if err = w.Service.Store.DeleteArtifactAssetObjects(ctx, a.ID, keys); err != nil {
			return false, err
		}
	}

	if len(keys) == 1000 {
		err = w.Service.Store.ReleaseArtifactGCClaim(ctx, a.ID, w.Replica)
		return false, err
	}
	return true, nil
}

// collectWorkerObjects bounds every object family produced by builds and edge
// publication. Metadata for artifacts is removed only after S3 cleanup
// succeeds, retaining enough information to retry after an outage.
func (w *Controller) CollectWorkerObjects(ctx context.Context) error {
	artifacts, err := w.Service.Store.GarbageArtifacts(ctx, time.Now().UTC().Add(-storage.WorkerArtifactRetain), 256)
	if err != nil {
		return err
	}
	var failures []error
	if err := w.RemoveWorkerObjects(ctx, "build-logs/", func(_ string, modified time.Time) bool {
		return !modified.IsZero() && modified.Before(time.Now().UTC().Add(-7*24*time.Hour))
	}); err != nil {
		failures = append(failures, err)
	}
	for _, artifact := range artifacts {
		if artifact.StorageLocation != w.Config.S3LocationID || artifact.Bucket != w.Config.S3Bucket {
			blocked, blockErr := w.Service.Store.BlockGarbageArtifact(ctx, artifact.ID, w.Replica, artifact.UseRevision)
			if blockErr != nil {
				failures = append(failures, blockErr)
			} else if blocked {
				failures = append(failures, fmt.Errorf("artifact %s cleanup blocked: storage location %q bucket %q is not managed by this controller", artifact.ID, artifact.StorageLocation, artifact.Bucket))
			}
			continue
		}
		claimed, err := w.Service.Store.ClaimGarbageArtifact(ctx, artifact.ID, w.Replica, artifact.UseRevision)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if !claimed {
			continue
		}
		if err := w.S3.RemoveObject(ctx, artifact.Bucket, artifact.ObjectKey, objectstore.RemoveObjectOptions{}); err != nil {
			failures = append(failures, err)
			continue
		}

		complete, err := w.CollectArtifactAssets(ctx, artifact)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if !complete {
			continue
		}
		if err := w.S3.RemoveObject(ctx, artifact.Bucket, WorkerArtifactManifestKey(artifact), objectstore.RemoveObjectOptions{}); err != nil {
			failures = append(failures, err)
			continue
		}

		if err := w.Service.Store.DeleteGarbageArtifact(ctx, artifact.ID, w.Replica); err != nil {
			failures = append(failures, err)
			continue
		}
		w.assetMu.Lock()
		delete(w.AssetCache, artifact.ID)
		w.assetMu.Unlock()
	}

	sources, sourceErr := w.Service.Store.GarbageWorkerSourceUploads(ctx, time.Now().UTC().Add(-storage.WorkerUploadRetain), 64)
	if sourceErr != nil {
		failures = append(failures, sourceErr)
	} else {
		for _, source := range sources {
			claimed, claimErr := w.Service.Store.ClaimGarbageWorkerSourceUpload(ctx, source.Key, w.Replica, source.UseRevision)
			if claimErr != nil {
				failures = append(failures, claimErr)
				continue
			}
			if !claimed {
				continue
			}
			if removeErr := w.S3.RemoveObject(ctx, w.Config.S3Bucket, source.Key, objectstore.RemoveObjectOptions{}); removeErr != nil {
				failures = append(failures, removeErr)
				continue
			}
			if deleteErr := w.Service.Store.DeleteGarbageWorkerSourceUpload(ctx, source.Key, w.Replica); deleteErr != nil {
				failures = append(failures, deleteErr)
			}
		}
	}

	if err := w.RemoveWorkerObjects(ctx, "uploads/", func(key string, modified time.Time) bool {
		return !strings.Contains(key, "/src/") && !modified.IsZero() && modified.Before(time.Now().UTC().Add(-storage.WorkerUploadRetain))
	}); err != nil {
		failures = append(failures, err)
	}

	_, floor, err := w.Service.Store.ConfigState(ctx)
	if err != nil {
		failures = append(failures, err)
	} else if floor > 0 {
		protected := map[string]bool{}
		checkpoints, e := w.Service.Store.PublishedConfigCheckpoints(ctx)
		if e != nil {
			return e
		}
		for _, checkpoint := range checkpoints {
			protected[WorkerPoolConfigPrefix(checkpoint.Pool)+"shards/"+checkpoint.Shard+"/"+strconv.FormatInt(checkpoint.PublishedRevision, 10)+".json.gz"] = true
		}
		if err := w.RemoveWorkerObjects(ctx, WorkerConfigPoolsDir, func(key string, _ time.Time) bool {
			if protected[key] {
				return false
			}
			if !strings.Contains(key, "/shards/") {
				return false
			}
			name := strings.TrimSuffix(path.Base(key), ".json.gz")
			revision, parseErr := strconv.ParseInt(name, 10, 64)
			return parseErr == nil && revision < floor
		}); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (w *Controller) RemoveWorkerObjects(ctx context.Context, prefix string, remove func(string, time.Time) bool) error {
	if w.objectGCAfter == nil {
		w.objectGCAfter = map[string]string{}
	}
	count := 0
	for object := range w.S3.ListObjects(ctx, w.Config.S3Bucket, objectstore.ListObjectsOptions{Prefix: prefix, Recursive: true, StartAfter: w.objectGCAfter[prefix], MaxKeys: 500}) {
		if object.Err != nil {
			return object.Err
		}
		if remove(object.Key, object.LastModified) {
			if err := w.S3.RemoveObject(ctx, w.Config.S3Bucket, object.Key, objectstore.RemoveObjectOptions{}); err != nil {
				return err
			}
		}
		w.objectGCAfter[prefix] = object.Key
		count++
		if count >= 500 {
			return nil
		}
	}
	delete(w.objectGCAfter, prefix)
	return nil
}
