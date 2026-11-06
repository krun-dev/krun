package workers

import (
	"context"
	"errors"
	"sort"
	"strconv"

	objectstore "github.com/krun-dev/krun/internal/adapters/objectstore"
	workerartifact "github.com/krun-dev/krun/internal/artifact"
	appconfig "github.com/krun-dev/krun/internal/config"
	storage "github.com/krun-dev/krun/internal/storage"
)

// Only the dirty worker batch is read and diffed. Queue generations protect a
// newer mutation from being acknowledged by an older publisher. The revision
// transaction contains database statements only; all S3 writes are retryable.
func (w *Controller) PublishConfigDelta(ctx context.Context) (int64, error) {
	for attempt := 0; attempt < 8; attempt++ {
		head, _, err := w.Service.Store.ConfigState(ctx)
		if err != nil {
			return 0, err
		}
		dirty, err := w.Service.Store.DirtyWorkers(ctx)
		if err != nil {
			return head, err
		}
		if len(dirty) == 0 {
			return head, w.WritePendingCheckpoints(ctx, head)
		}
		ids := make([]string, 0, len(dirty))
		for _, d := range dirty {
			ids = append(ids, d.ID)
		}
		next, err := LoadWorkerConfigProjection(ctx, w.Service.Store, ids, w.Config)
		if err != nil {
			return head, err
		}
		old, err := w.Service.Store.PublishedWorkers(ctx, ids)
		if err != nil {
			return head, err
		}
		ops := DiffWorkerConfig(&old, next)
		revision, won, err := w.Service.Store.AppendConfigDelta(ctx, head, dirty, ops)
		if err != nil {
			return head, err
		}
		if !won {
			continue
		}
		w.Service.Store.SignalConfig()
		return revision, w.WritePendingCheckpoints(ctx, revision)
	}
	return 0, errors.New("edge config publisher CAS contention")
}

func (w *Controller) WritePendingCheckpoints(ctx context.Context, head int64) error {
	if w.S3 == nil && w.PutObject == nil {
		return nil
	}
	if w.S3 != nil && !w.configBucket {
		if err := w.EnsureBucket(ctx); err != nil {
			return err
		}
		w.configBucket = true
	}
	pending, err := w.Service.Store.PendingConfigCheckpoints(ctx, 32)
	if err != nil {
		return err
	}
	for _, p := range pending {
		projection, snapshotRevision, e := w.Service.Store.WorkerConfigShard(ctx, p.Pool, p.Shard)
		if e != nil {
			return e
		}
		p.Revision = snapshotRevision
		raw, e := EncodeWorkerConfigSnapshot(p.Pool, p.Revision, projection)
		if e != nil {
			return e
		}
		relative := "shards/" + p.Shard + "/" + strconv.FormatInt(p.Revision, 10) + ".json.gz"
		key := WorkerPoolConfigPrefix(p.Pool) + relative
		if w.S3 != nil {
			e = w.PutImmutable(ctx, key, raw)
		} else {
			e = w.PutBytes(ctx, key, raw, objectstore.PutObjectOptions{ContentType: "application/gzip"})
		}
		if e != nil {
			return e
		}
		latest := WorkerConfigLatest{ConfigRevision: p.Revision, Snapshot: relative, SHA256: workerartifact.BytesDigest(raw)}
		if e = w.WriteConfigLatest(ctx, WorkerPoolConfigPrefix(p.Pool)+"shards/"+p.Shard+"/latest.json", latest); e != nil {
			return e
		}
		if e = w.Service.Store.MarkConfigCheckpointPublished(ctx, p.Pool, p.Shard, p.Revision); e != nil {
			return e
		}
	}
	pools := append([]appconfig.WorkerRuntimePool(nil), w.Config.ConfiguredRuntimePools()...)
	sort.Slice(pools, func(i, j int) bool { return pools[i].ID < pools[j].ID })
	for _, p := range pools {
		if err = w.WriteConfigLatest(ctx, WorkerPoolConfigLatestKey(p.ID), WorkerConfigLatest{LazyRoutes: true, ConfigRevision: head, OpsFloor: max(1, head-storage.WorkerConfigOpsRetain+1)}); err != nil {
			return err
		}
	}
	return nil
}
