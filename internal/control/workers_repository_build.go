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
	owned, err := w.server.Store.setClaimedBuildState(ctx, build.ID, w.replica, "building", "fetching")
	if err != nil {
		return build, private, err
	}
	if !owned {
		return build, private, errBuildClaimLost
	}
	build.State = "fetching"
	// Resolve the fetch credential once: the connecting user's OAuth grant,
	// falling back to the provider's shared PAT.
	fetchToken, err := w.server.repositoryCredential(ctx, provider, private.Actor.User)
	if err != nil {
		return build, private, err
	}
	if source.Commit == "" {
		commit, err := w.server.resolveRepositoryCommit(ctx, provider, source.WorkerRepositorySource, fetchToken)
		if err != nil {
			return build, private, err
		}
		source.Commit = commit
		build.Repository = &source
		// Pin the commit before downloading; retries must not follow a moving branch.
		owned, err := w.server.Store.setClaimedBuildRepository(ctx, build.ID, w.replica, source)
		if err != nil {
			return build, private, err
		}
		if !owned {
			return build, private, errBuildClaimLost
		}
	}
	files, archiveRaw, err := w.server.fetchRepositorySource(ctx, provider, source, fetchToken)
	if err != nil {
		return build, private, err
	}
	// A repository carrying wrangler config declares its own entrypoint and asset
	// directory. Resolve those before bundling so a standard Cloudflare project
	// builds without editing its layout or restating the config per build.
	wrangler, wranglerFile, err := parseWranglerConfig(files)
	if err != nil {
		return build, private, err
	}
	resolvedAssets, entrypoint, err := resolveWranglerBuild(wrangler, build.Assets, source.Entrypoint)
	if err != nil {
		return build, private, err
	}
	// Assets are read in a second pass: their directory is only known now.
	var assetFiles map[string][]byte
	if resolvedAssets.Directory != "" {
		assetFiles, err = extractRepositoryAssets(archiveRaw, source.RootDirectory, resolvedAssets.Directory)
		if err != nil {
			return build, private, err
		}
		if len(assetFiles) == 0 {
			return build, private, fmt.Errorf("Assets directory %s is missing or empty", resolvedAssets.Directory)
		}
	}
	if entrypoint == "" {
		return build, private, fmt.Errorf("No entrypoint: set one in the build request or declare `main` in wrangler config")
	}
	if err := sourceEntryValid(entrypoint, files); err != nil {
		return build, private, fmt.Errorf("Repository project does not contain the entrypoint %s", entrypoint)
	}
	// The config file itself is not source; drop it so the builder never sees it.
	for _, cfgName := range wranglerFileNames {
		delete(files, cfgName)
	}
	build.Assets = nil
	if resolvedAssets.Directory != "" || resolvedAssets.RunWorkerFirst != nil || resolvedAssets.NotFoundHandling != "" || resolvedAssets.HTMLHandling != "" {
		build.Assets = &resolvedAssets
	}
	// The builder receives b.Entrypoint, not the repository source, so the
	// resolved value has to be written back onto the build itself.
	build.Entrypoint = entrypoint
	source.Entrypoint = entrypoint
	build.Repository = &source
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
	if wranglerFile != "" {
		private.Log += fmt.Sprintf("Config: %s (entrypoint %s", wranglerFile, entrypoint)
		if resolvedAssets.Directory != "" {
			private.Log += fmt.Sprintf(", assets %s", resolvedAssets.Directory)
		}
		private.Log += ")\n"
	}
	// Authorization needs the IAM partition, so it runs before the Workers write.
	denied, authErr := w.authorizeActor(ctx, private.Actor, build.Service, build.Environment, "build.start")
	if authErr != nil {
		return build, private, authErr
	}
	if denied != nil {
		return build, private, fmt.Errorf("Build permission revoked")
	}
	err = w.server.Store.storeClaimedRepositoryBuild(ctx, build.ID, w.replica, build, private)
	return build, private, err
}
