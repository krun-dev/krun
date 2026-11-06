package workers

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/krun-dev/krun/internal/adapters/objectstore"
	"github.com/krun-dev/krun/internal/artifact"
	"github.com/krun-dev/krun/internal/domain"
)

// Each snapshot is immutable. A stale lease owner can upload an orphan but
// cannot replace the snapshot referenced by the current build owner.
func (w *Controller) SaveBuildLog(ctx context.Context, id, owner, text string) (string, error) {
	text = artifact.SafeWorkerLog(text)
	key := "build-logs/" + id + "/" + artifact.BytesDigest([]byte(owner)) + "/" + artifact.BytesDigest([]byte(text)) + ".txt"
	if err := w.EnsureBucket(ctx); err != nil {
		return "", err
	}
	err := w.PutBytes(ctx, key, []byte(text), objectstore.PutObjectOptions{ContentType: "text/plain; charset=utf-8", CacheControl: "private, no-store"})
	return key, err
}

func (w *Controller) PublishBuildLog(ctx context.Context, b domain.WorkerBuild, text string) error {
	w.localMu.Lock()
	if w.logPublished == nil {
		w.logPublished = map[string]time.Time{}
	}
	at := time.Now()
	for id, last := range w.logPublished {
		if at.Sub(last) >= 10*time.Second {
			delete(w.logPublished, id)
		}
	}
	if _, exists := w.logPublished[b.ID]; exists {
		w.localMu.Unlock()
		return nil
	}
	w.logPublished[b.ID] = at
	w.localMu.Unlock()
	key, err := w.SaveBuildLog(ctx, b.ID, w.Replica, text)
	if err != nil {
		return err
	}
	return w.Service.Store.SetClaimedBuildLogKey(ctx, b.ID, w.Replica, key)
}

func (w *Controller) FinishBuildArtifact(ctx context.Context, b domain.WorkerBuild, owner string, a domain.WorkerArtifact, text string) error {
	key, err := w.SaveBuildLog(ctx, b.ID, owner, text)
	if err != nil {
		return err
	}
	return w.Service.Store.FinishClaimedBuildWithArtifact(ctx, b.ID, owner, "succeeded", "", a, key)
}

func (w *Controller) BuildLogURL(ctx context.Context, key string) (string, error) {
	if key == "" {
		return "", nil
	}
	client := w.S3LogRead
	if client == nil {
		client = w.S3
	}
	u, err := client.PresignedGetObject(ctx, w.Config.S3Bucket, key, 5*time.Minute, nil)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

func (w *Controller) ReadBuildLog(ctx context.Context, key string) (string, error) {
	if key == "" {
		return "", nil
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	obj, err := w.S3.GetObject(ctx, w.Config.S3Bucket, key, objectstore.GetObjectOptions{})
	if err != nil {
		return "", err
	}
	defer obj.Close()

	raw, err := io.ReadAll(io.LimitReader(obj, 32769))
	if err != nil {
		return "", err
	}
	if len(raw) > 32768 {
		return "", errors.New("build log exceeds size limit")
	}
	return string(raw), nil
}
