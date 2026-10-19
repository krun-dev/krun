package control

import (
	"context"
	"encoding/json"
	"fmt"
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
	if source.Commit == "" {
		commit, err := w.server.resolveRepositoryCommit(ctx, provider, source.WorkerRepositorySource)
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
	files, err := w.server.fetchRepositorySource(ctx, provider, source)
	if err != nil {
		return build, private, err
	}
	raw, err := json.Marshal(files)
	if err != nil {
		return build, private, err
	}
	build.SourceDigest = bytesDigest(raw)
	build.State = "fetching"
	private.Files = files
	private.Log = fmt.Sprintf("Repository: %s/%s\nRef: %s\nCommit: %s\nProject: %s\nLoaded %d source files\n", source.ProviderID, source.Repository, source.Ref, source.Commit, source.RootDirectory, len(files))
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
