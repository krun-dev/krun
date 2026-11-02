package control

import (
	"context"
	"errors"
	"sort"
	"strconv"

	"github.com/minio/minio-go/v7"
)

// Only the dirty service batch is read and diffed. Queue generations protect a
// newer mutation from being acknowledged by an older publisher. The revision
// transaction contains database statements only; all S3 writes are retryable.
func (w *workersController) publishConfigDelta(ctx context.Context) (int64, error) {
	for attempt := 0; attempt < 8; attempt++ {
		head, _, err := w.server.Store.ConfigState(ctx)
		if err != nil {
			return 0, err
		}
		dirty, err := w.server.Store.DirtyWorkerServices(ctx)
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
		next, err := loadWorkerConfigProjection(ctx, w.server.Store, ids, w.config)
		if err != nil {
			return head, err
		}
		old, err := w.server.Store.PublishedWorkerServices(ctx, ids)
		if err != nil {
			return head, err
		}
		ops := diffWorkerConfig(&old, next)
		revision, won, err := w.server.Store.AppendConfigDelta(ctx, head, dirty, ops)
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
	pending, err := w.server.Store.PendingConfigCheckpoints(ctx, 32)
	if err != nil {
		return err
	}
	for _, p := range pending {
		projection, snapshotRevision, e := w.server.Store.WorkerConfigShard(ctx, p.Pool, p.Shard)
		if e != nil {
			return e
		}
		p.Revision = snapshotRevision
		raw, e := encodeWorkerConfigSnapshot(p.Pool, p.Revision, projection)
		if e != nil {
			return e
		}
		relative := "shards/" + p.Shard + "/" + strconv.FormatInt(p.Revision, 10) + ".json.gz"
		key := workerPoolConfigPrefix(p.Pool) + relative
		if w.s3 != nil {
			e = w.putImmutable(ctx, key, raw)
		} else {
			e = w.putBytes(ctx, key, raw, minio.PutObjectOptions{ContentType: "application/gzip"})
		}
		if e != nil {
			return e
		}
		latest := workerConfigLatest{ConfigRevision: p.Revision, Snapshot: relative, SHA256: bytesDigest(raw)}
		if e = w.writeConfigLatest(ctx, workerPoolConfigPrefix(p.Pool)+"shards/"+p.Shard+"/latest.json", latest); e != nil {
			return e
		}
		if e = w.server.Store.MarkConfigCheckpointPublished(ctx, p.Pool, p.Shard, p.Revision); e != nil {
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
