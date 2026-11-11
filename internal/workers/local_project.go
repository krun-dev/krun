package workers

import (
	"context"

	"github.com/krun-dev/krun/internal/adapters/objectstore"
	"github.com/krun-dev/krun/internal/authorization"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/securetoken"
)

// LocalBuildSource is prepared by the server from a validated project archive.
type LocalBuildSource struct {
	Key                string
	SourceKey          string
	Digest             string
	ConfigPath         string
	Entrypoint         string
	CompatibilityDate  string
	CompatibilityFlags []string
}

func (s *Service) CheckLocalProjectTarget(st *domain.State, p authorization.Principal, ref WorkerRef) error {
	target, err := s.WorkerTarget(st, ref, ref.Environment, "build.start", p)
	if err != nil {
		return err
	}
	env, exists := s.Worker(st, target).Environments[ref.Environment]
	if !exists {
		return domain.NotFound()
	}
	if !env.Enabled {
		return domain.Conflict("ENVIRONMENT_DISABLED", "Target environment is disabled")
	}
	return nil
}

func (s *Service) FindLocalProjectBuild(st *domain.State, p authorization.Principal, ref WorkerRef, source LocalBuildSource) (domain.WorkerBuild, error) {
	target, err := s.WorkerTarget(st, ref, ref.Environment, "build.start", p)
	if err != nil {
		return domain.WorkerBuild{}, err
	}
	key := WorkerIdempotencyKey(target, p, "local-build", ref.Environment, source.Key)
	id, err := WorkerIdempotent(st, key, source.Digest)
	if err != nil || id == "" {
		return domain.WorkerBuild{}, err
	}
	build, ok := st.Workers.Builds[id]
	if !ok {
		return domain.WorkerBuild{}, domain.Conflict("BUILD_UNAVAILABLE", "The original upload build is no longer available")
	}
	return build, nil
}

func (s *Service) QueueLocalProject(ctx context.Context, st *domain.State, p authorization.Principal, ref WorkerRef, source LocalBuildSource) (domain.WorkerBuild, error) {
	in := WorkerBuildCreateInput{
		SourceType: "local-project", Environment: ref.Environment, SourceUpload: source.SourceKey,
		Entrypoint: source.Entrypoint, CompatibilityDate: source.CompatibilityDate,
		CompatibilityFlags: source.CompatibilityFlags, Key: source.Key,
	}
	out, _, err := s.workerBuildCreate(ctx, st, p, ref, in, &source)
	if err != nil {
		return domain.WorkerBuild{}, err
	}
	return out.(domain.WorkerBuild), nil
}

func (w *Controller) StageLocalProject(ctx context.Context, organization string, bundle []byte) (string, error) {
	if err := w.EnsureBucket(ctx); err != nil {
		return "", err
	}
	key := WorkerSourceUploadKey(organization, securetoken.NewID("src"))
	if err := w.Service.Store.IssueWorkerSourceUpload(ctx, key, organization); err != nil {
		return "", err
	}
	if err := w.PutBytes(ctx, key, bundle, objectstore.PutObjectOptions{ContentType: "application/gzip"}); err != nil {
		return "", err
	}
	return key, nil
}
