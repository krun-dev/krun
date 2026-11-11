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
			return head, w.WritePendingCheckpoints(ctx)
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
		return revision, w.WritePendingCheckpoints(ctx)
	}
	return 0, errors.New("edge config publisher CAS contention")
}

func (w *Controller) WritePendingCheckpoints(ctx context.Context) error {
	if w.S3 == nil && w.PutObject == nil {
		return nil
	}
	if w.S3 != nil && !w.configBucket {
		if err := w.EnsureBucket(ctx); err != nil {
			return err
		}
		w.configBucket = true
	}
	head, err := w.Service.Store.ConfigPosition(ctx)
	if err != nil {
		return err
	}
	pending, err := w.Service.Store.PendingConfigCheckpoints(ctx, 32)
	if err != nil {
		return err
	}
	for _, p := range pending {
		projection, position, e := w.Service.Store.EdgeConfigShard(ctx, p.Pool, p.Shard)
		if e != nil {
			return e
		}
		if position.Epoch != head.Epoch {
			return errors.New("configuration epoch changed during checkpoint publication")
		}
		raw, e := EncodeWorkerConfigSnapshot(p.Pool, position, projection)
		if e != nil {
			return e
		}
		relative := "shards/" + p.Shard + "/" + strconv.FormatInt(position.Revision, 10) + ".json.gz"
		prefix := WorkerPoolConfigPrefix(p.Pool, position.Epoch)
		key := prefix + relative
		if w.S3 != nil {
			e = w.PutImmutable(ctx, key, raw)
		} else {
			e = w.PutBytes(ctx, key, raw, objectstore.PutObjectOptions{ContentType: "application/gzip"})
		}
		if e != nil {
			return e
		}
		latest := EdgeConfigLatest{Epoch: position.Epoch, ConfigRevision: position.Revision, Snapshot: relative, SHA256: workerartifact.BytesDigest(raw)}
		if e = w.WriteConfigLatest(ctx, prefix+"shards/"+p.Shard+"/latest.json", latest); e != nil {
			return e
		}
		if e = w.Service.Store.MarkConfigCheckpointPublished(ctx, p.Pool, p.Shard, position); e != nil {
			return e
		}
	}
	pools := append([]appconfig.WorkerRuntimePool(nil), w.Config.ConfiguredRuntimePools()...)
	sort.Slice(pools, func(i, j int) bool { return pools[i].ID < pools[j].ID })
	for _, p := range pools {
		if err = w.WriteConfigLatest(ctx, WorkerPoolConfigLatestKey(p.ID, head.Epoch), EdgeConfigLatest{
			Epoch: head.Epoch, LazyRoutes: true, ConfigRevision: head.Revision, OpsFloor: max(1, head.Revision-storage.EdgeConfigOpsRetain+1),
		}); err != nil {
			return err
		}
	}
	return nil
}
