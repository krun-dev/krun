package workers

import (
	"context"
	"fmt"

	objectstore "github.com/krun-dev/krun/internal/adapters/objectstore"
	workerartifact "github.com/krun-dev/krun/internal/artifact"
	domain "github.com/krun-dev/krun/internal/domain"
	storage "github.com/krun-dev/krun/internal/storage"
)

func (w *Controller) PrepareRepositoryBuild(ctx context.Context, build domain.WorkerBuild, private domain.WorkerBuildPrivate) (domain.WorkerBuild, domain.WorkerBuildPrivate, error) {
	source := *build.Repository
	provider, err := w.Service.Repository.ValidateRepositorySource(&source.WorkerRepositorySource, build.Organization)
	if err != nil {
		return build, private, fmt.Errorf("Repository provider or repository is no longer enabled for this build")
	}
	owned, err := w.Service.Store.SetClaimedBuildState(ctx, build.ID, w.Replica, "building", "fetching")
	if err != nil {
		return build, private, err
	}
	if !owned {
		return build, private, storage.ErrBuildClaimLost
	}
	build.State = "fetching"
	// Resolve the fetch credential once: the connecting user's OAuth grant,
	// falling back to the provider's shared PAT.
	fetchToken, err := w.Service.Repository.RepositoryCredential(ctx, provider, private.Actor.User)
	if err != nil {
		return build, private, err
	}
	if source.Commit == "" {
		commit, err := w.Service.Repository.ResolveRepositoryCommit(ctx, provider, source.WorkerRepositorySource, fetchToken)
		if err != nil {
			return build, private, err
		}
		source.Commit = commit
		build.Repository = &source
		// Pin the commit before downloading; retries must not follow a moving branch.
		owned, err := w.Service.Store.SetClaimedBuildRepository(ctx, build.ID, w.Replica, source)
		if err != nil {
			return build, private, err
		}
		if !owned {
			return build, private, storage.ErrBuildClaimLost
		}
	}
	files, _, err := w.Service.Repository.FetchRepositorySource(ctx, provider, source, fetchToken)
	if err != nil {
		return build, private, err
	}
	// Repository builds require an explicit Cloudflare Workers configuration.
	// Wrangler's framework auto-configuration intentionally is not part of this
	// platform: users submit a Workers-ready project and the isolated builder
	// only performs its declared build.
	wrangler, wranglerFile, err := workerartifact.ParseWranglerConfig(files)
	if err != nil {
		return build, private, err
	}
	if wranglerFile == "" {
		return build, private, fmt.Errorf("Repository project must include wrangler.jsonc, wrangler.json or wrangler.toml")
	}
	resolvedAssets, entrypoint, err := workerartifact.ResolveWranglerBuild(wrangler)
	if err != nil {
		return build, private, err
	}
	if resolvedAssets.Directory != "" || resolvedAssets.Binding != "" || resolvedAssets.RunWorkerFirst != nil || resolvedAssets.NotFoundHandling != "" || resolvedAssets.HTMLHandling != "" {
		if err := workerartifact.ValidateWranglerInputAssets(resolvedAssets); err != nil {
			return build, private, fmt.Errorf("Invalid Worker assets configuration: %w", err)
		}
	}
	if build.CompatibilityDate == "" {
		build.CompatibilityDate, build.CompatibilityFlags, err = workerartifact.ResolveWranglerCompatibility(wrangler)
	} else {
		build.CompatibilityDate, build.CompatibilityFlags, err = workerartifact.NormalizeWorkerCompatibility(build.CompatibilityDate, build.CompatibilityFlags)
	}
	if err != nil {
		return build, private, fmt.Errorf("Invalid Worker compatibility configuration: %w", err)
	}
	// Do not require main to exist before the build. Wrangler custom builds and
	// Vite/framework adapters commonly generate dist/worker.js (and assets) as
	// part of their checked-in build command.
	if entrypoint == "" && resolvedAssets.Directory == "" {
		// Do not try to guess a framework here. A custom/Vite build remains valid
		// because Wrangler will resolve its generated deployment configuration;
		// requiring package.json prevents an empty config from masquerading as one.
		if _, ok := files["package.json"]; !ok {
			return build, private, fmt.Errorf("Wrangler configuration must declare main or assets, or the project must declare a build in package.json")
		}
	}
	build.Assets = nil
	// Only persist a complete pre-build assets configuration. Vite-generated
	// assets are taken from the validated final artifact instead of storing an
	// intentionally incomplete binding-only declaration on the Build row.
	if resolvedAssets.Directory != "" {
		build.Assets = &resolvedAssets
	}
	// Keep the declared entrypoint as build metadata. Wrangler remains
	// authoritative and may replace it with generated deployment config.
	build.Entrypoint = entrypoint
	source.ConfigPath = wranglerFile
	build.Repository = &source
	// Keep the complete project (including Wrangler config, lockfiles and binary
	// modules) under src/. The builder distinguishes this from inline direct
	// source by the build_mode/config_path input fields.
	bundle, err := WorkerSourceBundle(files, nil)
	if err != nil {
		return build, private, err
	}
	// The bundle goes to object storage; state and the builder ConfigMap only
	// carry its key. This keeps large asset trees out of PostgreSQL and ConfigMaps.
	sourceKey := WorkerSourceUploadKey(build.Organization, build.ID)
	if err := w.PutBytes(ctx, sourceKey, bundle, objectstore.PutObjectOptions{ContentType: "application/gzip"}); err != nil {
		return build, private, err
	}
	build.SourceDigest = workerartifact.BytesDigest(bundle)
	build.State = "fetching"
	private.Files = nil
	private.SourceKey = sourceKey
	private.Log = fmt.Sprintf("Repository: %s/%s\nRef: %s\nCommit: %s\nProject: %s\nLoaded %d project files\n", source.ProviderID, source.Repository, source.Ref, source.Commit, source.RootDirectory, len(files))
	if wranglerFile != "" {
		private.Log += fmt.Sprintf("Config: %s (entrypoint %s", wranglerFile, entrypoint)
		if resolvedAssets.Directory != "" {
			private.Log += fmt.Sprintf(", assets %s", resolvedAssets.Directory)
		}
		private.Log += ")\n"
	}
	private.Log += fmt.Sprintf("Compatibility: %s", build.CompatibilityDate)
	if len(build.CompatibilityFlags) > 0 {
		private.Log += fmt.Sprintf(" (%v)", build.CompatibilityFlags)
	}
	private.Log += "\n"
	// Authorization needs the IAM partition, so it runs before the Workers write.
	denied, authErr := w.AuthorizeActor(ctx, private.Actor, build.Worker, build.Environment, "build.start")
	if authErr != nil {
		return build, private, authErr
	}
	if denied != nil {
		return build, private, fmt.Errorf("Build permission revoked")
	}
	err = w.Service.Store.StoreClaimedRepositoryBuild(ctx, build.ID, w.Replica, build, private)
	return build, private, err
}
