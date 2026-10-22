package control

import (
	"context"
	"fmt"

	"github.com/minio/minio-go/v7"
)

func (w *workersController) prepareRepositoryBuild(ctx context.Context, build WorkerBuild, private WorkerBuildPrivate) (WorkerBuild, WorkerBuildPrivate, error) {
	source := *build.Repository
	provider, err := w.server.validateRepositorySource(&source.WorkerRepositorySource, build.Organization)
	if err != nil {
		return build, private, fmt.Errorf("Repository provider or repository is no longer enabled for this build")
	}
	if err := w.server.Store.Update(ctx, func(st *State) error {
		current := st.Workers.Builds[build.ID]
		current.State = "fetching"
		st.Workers.Builds[build.ID] = current
		return nil
	}); err != nil {
		return build, private, err
	}
	// Resolve the fetch credential once: the connecting user's OAuth grant,
	// falling back to the provider's shared PAT.
	var fetchToken string
	_ = w.server.Store.Update(ctx, func(st *State) error {
		actor := st.Workers.OperationPrivate[build.ID].Actor.User
		fetchToken = w.server.repositoryCredential(st, provider, actor)
		return nil
	})
	if source.Commit == "" {
		commit, err := w.server.resolveRepositoryCommit(ctx, provider, source.WorkerRepositorySource, fetchToken)
		if err != nil {
			return build, private, err
		}
		source.Commit = commit
		build.Repository = &source
		// Pin the commit before downloading; retries must not follow a moving branch.
		if err := w.server.Store.Update(ctx, func(st *State) error {
			current := st.Workers.Builds[build.ID]
			current.Repository = &source
			st.Workers.Builds[build.ID] = current
			return nil
		}); err != nil {
			return build, private, err
		}
	}
	files, assetFiles, err := w.server.fetchRepositorySource(ctx, provider, source, fetchToken, build.Assets)
	if err != nil {
		return build, private, err
	}
	bundle, err := workerSourceBundle(files, assetFiles)
	if err != nil {
		return build, private, err
	}
	// The bundle goes to object storage; state and the builder ConfigMap only
	// carry its key. This keeps large asset trees out of PostgreSQL and ConfigMaps.
	sourceKey := workerSourceUploadKey(build.Organization, build.ID)
	if err := w.putBytes(ctx, sourceKey, bundle, minio.PutObjectOptions{ContentType: "application/gzip"}); err != nil {
		return build, private, err
	}
	build.SourceDigest = bytesDigest(bundle)
	build.State = "fetching"
	private.Files = nil
	private.SourceKey = sourceKey
	private.Log = fmt.Sprintf("Repository: %s/%s\nRef: %s\nCommit: %s\nProject: %s\nLoaded %d source files and %d asset files\n", source.ProviderID, source.Repository, source.Ref, source.Commit, source.RootDirectory, len(files), len(assetFiles))
	err = w.server.Store.Update(ctx, func(st *State) error {
		if err := w.server.workerAuthorizeActor(st, private.Actor, build.Service, build.Environment, "build.start"); err != nil {
			return fmt.Errorf("Build permission revoked")
		}
		st.Workers.Builds[build.ID] = build
		st.Workers.BuildPrivate[build.ID] = private
		return nil
	})
	return build, private, err
}
