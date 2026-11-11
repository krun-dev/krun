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
	workCtx, guard := w.guardBuildLease(ctx, b.ID)
	err := w.ProcessBuild(workCtx, b, private)
	if lost := guard.stop(); err == nil {
		err = lost
	}
	return err
}

func (w *Controller) FailBuild(ctx context.Context, id, message, log string) error {
	key, err := w.SaveBuildLog(ctx, id, w.Replica, log)
	if err != nil {
		return err
	}
	return w.Service.Store.FinishClaimedBuild(ctx, id, w.Replica, "failed", message, "", key)
}

func (w *Controller) ProcessBuild(ctx context.Context, b domain.WorkerBuild, private domain.WorkerBuildPrivate) error {
	denied, e := w.AuthorizeActor(ctx, private.Actor, b.Worker, b.Environment, "build.start")
	if e != nil {
		return e
	}
	if denied != nil {
		return w.FailBuild(ctx, b.ID, "Build permission revoked", "")
	}
	if w.Config.BuilderMode == "local" {
		w.localMu.Lock()
		running := w.localBuilds[b.ID] != nil && w.localBuilds[b.ID].Running
		w.localMu.Unlock()
		if running {
			return nil
		}
	}
	started, startErr := w.Service.Store.BuildExecutionStarted(ctx, b.ID, b.Created)
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
			return w.FailBuild(ctx, b.ID, err.Error(), repositoryBuildLog(b))
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
		return w.FailBuild(ctx, b.ID, "Builder failed", repositoryBuildLog(b)+w.Kube.BuildLog(ctx, b.Job))
	}
	if job.Status.Succeeded == 0 {
		return w.PublishBuildLog(ctx, b, repositoryBuildLog(b)+w.Kube.BuildLog(ctx, b.Job))
	}
	artifact, e := w.FinalizeBuild(ctx, b)
	if e != nil {
		if errors.Is(e, storage.ErrArtifactBeingCollected) {
			return nil
		}
		return w.FailBuild(ctx, b.ID, "Artifact validation failed: "+e.Error(), repositoryBuildLog(b)+w.Kube.BuildLog(ctx, b.Job))
	}
	log := workerartifact.SafeWorkerLog(repositoryBuildLog(b) + w.Kube.BuildLog(ctx, b.Job))

	denied, e = w.AuthorizeActor(ctx, private.Actor, b.Worker, b.Environment, "build.start")
	if e != nil {
		return e
	}
	if denied != nil {
		return w.FailBuild(ctx, b.ID, "Build permission revoked", log)
	}
	e = w.FinishBuildArtifact(ctx, b, w.Replica, artifact, log)
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
	timeoutCtx, cancelTimeout := context.WithTimeout(parent, time.Duration(timeout)*time.Second)
	defer cancelTimeout()
	ctx, guard := w.guardBuildLease(timeoutCtx, b.ID)
	defer guard.stop()
	log, e := w.ExecuteLocalBuilder(ctx, b, private)
	// Persist timeout diagnostics using an independent bounded cleanup context.
	finishCtx := ctx
	finishCancel := func() {}
	if ctx.Err() != nil {
		finishCtx, finishCancel = context.WithTimeout(context.Background(), 15*time.Second)
	}
	defer finishCancel()
	log = repositoryBuildLog(b) + log
	if guard.lostErr() != nil {
		return
	}
	if e != nil {
		_ = w.FailBuild(finishCtx, b.ID, "Builder failed", log)
		return
	}
	artifact, e := w.FinalizeBuild(finishCtx, b)
	if e != nil {
		if errors.Is(e, storage.ErrArtifactBeingCollected) {
			return
		}
		_ = w.FailBuild(finishCtx, b.ID, "Artifact validation failed: "+e.Error(), log)
		return
	}

	denied, e := w.AuthorizeActor(finishCtx, private.Actor, b.Worker, b.Environment, "build.start")
	if e != nil {
		_ = w.FailBuild(finishCtx, b.ID, "Build permission check unavailable", log)
		return
	}
	if denied != nil {
		_ = w.FailBuild(finishCtx, b.ID, "Build permission revoked", log)
		return
	}
	e = w.FinishBuildArtifact(finishCtx, b, w.Replica, artifact, log)
	if e == nil {
		_ = w.S3.RemoveObject(finishCtx, w.Config.S3Bucket, WorkerUploadKey(b), objectstore.RemoveObjectOptions{})
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
	return localbuilder.Execute(ctx, w.Config, b, private, source, upload, func(text string) {
		_ = w.PublishBuildLog(ctx, b, repositoryBuildLog(b)+text)
	})
}

var LocalBuilderEnvironment = localbuilder.LocalBuilderEnvironment
