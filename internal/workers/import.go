package workers

import (
	"context"
	"time"

	"github.com/krun-dev/krun/internal/adapters/objectstore"
	workerartifact "github.com/krun-dev/krun/internal/artifact"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/securetoken"
)

func (w *Controller) ImportModules(ctx context.Context, ref ServiceRef, actor domain.WorkerActor, raw []byte) (domain.WorkerBuild, error) {
	code, _, err := workerartifact.ParseWorkerArchive(raw)
	if err != nil {
		return domain.WorkerBuild{}, err
	}
	at := time.Now().UTC()
	owner := "upload-" + securetoken.UUID()
	b := domain.WorkerBuild{ID: securetoken.NewID("bld"), Organization: ref.Organization, Service: ref.Service, Environment: ref.Environment, State: "building", Entrypoint: code.MainModule, Profile: code.Profile, CompatibilityDate: code.CompatibilityDate, CompatibilityFlags: code.CompatibilityFlags, Created: at, ClaimedBy: owner, ClaimedAt: &at}
	if err = w.Service.Store.StartUploadedBuild(ctx, b, actor); err != nil {
		return b, err
	}
	defer func() {
		if err != nil {
			cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = w.Service.Store.FinishClaimedBuild(cleanup, b.ID, owner, "failed", "Module upload failed", "", "")
		}
	}()
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = w.S3.RemoveObject(cleanup, w.Config.S3Bucket, WorkerUploadKey(b), objectstore.RemoveObjectOptions{})
	}()
	if err = w.EnsureBucket(ctx); err != nil {
		return b, err
	}
	if err = w.PutBytes(ctx, WorkerUploadKey(b), raw, objectstore.PutObjectOptions{ContentType: "application/gzip"}); err != nil {
		return b, err
	}
	artifact, err := w.FinalizeUploadedBuild(ctx, b, owner)
	if err != nil {
		return b, err
	}
	if err = w.Service.Store.FinishClaimedBuildWithArtifact(ctx, b.ID, owner, "succeeded", "", artifact, "Bundled module upload accepted"); err != nil {
		return b, err
	}
	b.State = "succeeded"
	b.Artifact = artifact.ID
	return b, nil
}
