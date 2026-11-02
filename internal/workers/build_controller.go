package workers

import (
	"context"
	"errors"
	"time"

	kubernetes "github.com/krun-dev/krun/internal/adapters/kubernetes"
	localbuilder "github.com/krun-dev/krun/internal/adapters/localbuilder"
	objectstore "github.com/krun-dev/krun/internal/adapters/objectstore"
	workerartifact "github.com/krun-dev/krun/internal/artifact"
	domain "github.com/krun-dev/krun/internal/domain"
	storage "github.com/krun-dev/krun/internal/storage"
)

func (w *Controller) ProcessClaimedBuild(ctx context.Context, b domain.WorkerBuild, private domain.WorkerBuildPrivate) error {
	workCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	leaseErr := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-workCtx.Done():
				return
			case <-ticker.C:
				owned, err := w.Service.Store.RenewBuildClaim(workCtx, b.ID, w.Replica)
				if err == nil && owned {
					continue
				}
				if err == nil {
					err = errors.New("build lease lost")
				}
				select {
				case <-done:
					return
				default:
					leaseErr <- err
					cancel()
					return
				}
			}
		}
	}()
	err := w.ProcessBuild(workCtx, b, private)
	close(done)
	cancel()
	select {
	case lost := <-leaseErr:
		if err == nil {
			return lost
		}
	default:
	}
	return err
}

func (w *Controller) FailBuild(ctx context.Context, id, message, log string) error {
	return w.Service.Store.FinishClaimedBuild(ctx, id, w.Replica, "failed", message, "", workerartifact.SafeWorkerLog(log))
}

func (w *Controller) ProcessBuild(ctx context.Context, b domain.WorkerBuild, private domain.WorkerBuildPrivate) error {
	denied, e := w.AuthorizeActor(ctx, private.Actor, b.Service, b.Environment, "build.start")
	if e != nil {
		return e
	}
	if denied != nil {
		return w.FailBuild(ctx, b.ID, "Build permission revoked", "")
	}
	started, startErr := w.Service.Store.WorkerExecutionStarted(ctx, "build", b.ID, b.Created)
	if startErr != nil {
		return startErr
	}
	timeout := w.Config.BuilderTimeoutSeconds
	if timeout == 0 {
		timeout = 600
	}
	if time.Now().UTC().Sub(started) > time.Duration(timeout)*time.Second {
		log := ""
		if w.Config.BuilderMode != "local" {
			log = w.Kube.BuildLog(ctx, b.Job)
		}
		return w.FailBuild(ctx, b.ID, "Build deadline exceeded", log)
	}
	if b.Repository != nil && private.SourceKey == "" {
		var err error
		b, private, err = w.PrepareRepositoryBuild(ctx, b, private)
		if err != nil {
			if errors.Is(err, storage.ErrBuildClaimLost) || ctx.Err() != nil {
				return err
			}
			return w.FailBuild(ctx, b.ID, err.Error(), private.Log)
		}
	}

	if private.SourceKey == "" && len(private.Files) > 0 {
		bundle, err := WorkerSourceBundle(private.Files, nil)
		if err != nil {
			return w.FailBuild(ctx, b.ID, err.Error(), "")
		}
		if e := w.EnsureBucket(ctx); e != nil {
			return e
		}
		key := WorkerSourceUploadKey(b.Organization, b.ID)
		if e := w.PutBytes(ctx, key, bundle, objectstore.PutObjectOptions{ContentType: "application/gzip"}); e != nil {
			return e
		}
		private.SourceKey = key
		private.Files = nil
		if e := w.Service.Store.SetBuildSource(ctx, b.ID, key); e != nil {
			return e
		}
	}
	if w.Config.BuilderMode == "local" {
		return w.ProcessLocalBuild(ctx, b, private)
	}
	var job kubernetes.WorkerJob
	e = w.Kube.Call(ctx, "GET", w.Kube.Jobs()+"/"+b.Job, nil, &job)
	if kubernetes.KubeMissing(e) {
		if e = w.EnsureBucket(ctx); e != nil {
			return e
		}
		signed, e := w.UploadURL(ctx, b)
		if e != nil {
			return e
		}
		sourceURL, e := w.SourceBundleURL(ctx, private.SourceKey)
		if e != nil {
			return e
		}
		if e = w.Kube.EnsureBuildJob(ctx, b, private, sourceURL, signed, w.Config.BuilderImage, w.Config.BuilderPullPolicy); e != nil {
			return e
		}
		return nil
	}
	if e != nil {
		return e
	}
	if job.Status.Failed > 0 {
		return w.FailBuild(ctx, b.ID, "Builder failed", w.Kube.BuildLog(ctx, b.Job))
	}
	if job.Status.Succeeded == 0 {
		return nil
	}
	artifact, e := w.FinalizeBuild(ctx, b)
	if e != nil {
		if errors.Is(e, storage.ErrArtifactBeingCollected) {
			return nil
		}
		return w.FailBuild(ctx, b.ID, "Artifact validation failed: "+e.Error(), w.Kube.BuildLog(ctx, b.Job))
	}
	log := workerartifact.SafeWorkerLog(private.Log + w.Kube.BuildLog(ctx, b.Job))

	denied, e = w.AuthorizeActor(ctx, private.Actor, b.Service, b.Environment, "build.start")
	if e != nil {
		return e
	}
	if denied != nil {
		return w.FailBuild(ctx, b.ID, "Build permission revoked", log)
	}
	e = w.Service.Store.FinishClaimedBuildWithArtifact(ctx, b.ID, w.Replica, "succeeded", "", artifact, log)
	if e == nil {
		_ = w.S3.RemoveObject(ctx, w.Config.S3Bucket, WorkerUploadKey(b), objectstore.RemoveObjectOptions{})
	}
	return e
}

// processLocalBuild runs workers/builder/build-worker.mjs on the control-plane
// host. It is the dev-mode executor: no image build and no Kubernetes Job.
func (w *Controller) ProcessLocalBuild(ctx context.Context, b domain.WorkerBuild, private domain.WorkerBuildPrivate) error {
	w.localMu.Lock()
	if state := w.localBuilds[b.ID]; state != nil && state.Running {
		w.localMu.Unlock()
		return nil
	}
	w.localBuilds[b.ID] = &WorkerLocalBuild{Running: true}
	w.localMu.Unlock()
	w.localWG.Add(1)
	go func() {
		defer w.localWG.Done()
		w.RunLocalBuild(b, private)
	}()
	return nil
}

func (w *Controller) RunLocalBuild(b domain.WorkerBuild, private domain.WorkerBuildPrivate) {
	defer func() {
		w.localMu.Lock()
		delete(w.localBuilds, b.ID)
		w.localMu.Unlock()
	}()
	parent := w.lifecycle
	if parent == nil {
		parent = context.Background()
	}
	timeout := w.Config.BuilderTimeoutSeconds
	if timeout == 0 {
		timeout = 600
	}
	ctx, cancel := context.WithTimeout(parent, time.Duration(timeout)*time.Second)
	defer cancel()
	done := make(chan struct{})
	defer close(done)
	leaseLost := make(chan struct{}, 1)
	go func() {
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				owned, err := w.Service.Store.RenewBuildClaim(ctx, b.ID, w.Replica)
				if err == nil && owned {
					continue
				}
				leaseLost <- struct{}{}
				cancel()
				return
			}
		}
	}()
	log, e := w.ExecuteLocalBuilder(ctx, b, private)
	select {
	case <-leaseLost:

		return
	default:
	}
	if e != nil {
		_ = w.FailBuild(ctx, b.ID, "Builder failed", log)
		return
	}
	artifact, e := w.FinalizeBuild(ctx, b)
	if e != nil {
		if errors.Is(e, storage.ErrArtifactBeingCollected) {
			return
		}
		_ = w.FailBuild(ctx, b.ID, "Artifact validation failed: "+e.Error(), log)
		return
	}

	denied, e := w.AuthorizeActor(ctx, private.Actor, b.Service, b.Environment, "build.start")
	if e != nil {
		_ = w.FailBuild(ctx, b.ID, "Build permission check unavailable", log)
		return
	}
	if denied != nil {
		_ = w.FailBuild(ctx, b.ID, "Build permission revoked", log)
		return
	}
	e = w.Service.Store.FinishClaimedBuildWithArtifact(ctx, b.ID, w.Replica, "succeeded", "", artifact, workerartifact.SafeWorkerLog(private.Log+log))
	if e == nil {
		_ = w.S3.RemoveObject(ctx, w.Config.S3Bucket, WorkerUploadKey(b), objectstore.RemoveObjectOptions{})
	}
}

func (w *Controller) ExecuteLocalBuilder(ctx context.Context, b domain.WorkerBuild, private domain.WorkerBuildPrivate) (string, error) {
	if e := w.EnsureBucket(ctx); e != nil {
		return "", e
	}
	upload, e := w.UploadURL(ctx, b)
	if e != nil {
		return "", e
	}
	source, e := w.SourceBundleURL(ctx, private.SourceKey)
	if e != nil {
		return "", e
	}
	return localbuilder.Execute(ctx, w.Config, b, source, upload)
}

var LocalBuilderEnvironment = localbuilder.LocalBuilderEnvironment
