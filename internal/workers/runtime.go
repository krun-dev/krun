package workers

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	workerartifact "github.com/krun-dev/krun/internal/artifact"
	domain "github.com/krun-dev/krun/internal/domain"
)

type WorkerVersionAssets struct {
	Version  domain.WorkerVersion
	Artifact domain.WorkerArtifact
	Config   *domain.WorkerAssetsConfig
	Files    []workerartifact.WorkerManifestFile
}

func (w *Controller) VersionAssets(ctx context.Context, versionID, pool string) (WorkerVersionAssets, error) {
	var out WorkerVersionAssets
	rel, art, e := w.Service.Store.RuntimeVersion(ctx, versionID, pool)
	out.Version, out.Artifact = rel, art

	if e != nil {
		return out, e
	}
	value, e, _ := w.assetFlights.Do(out.Artifact.ID, func() (any, error) {
		w.assetMu.Lock()
		cached, ok := w.AssetCache[out.Artifact.ID]
		if ok {
			cached.Used = time.Now().UTC()
			w.AssetCache[out.Artifact.ID] = cached
		}
		w.assetMu.Unlock()
		if ok {
			return cached, nil
		}
		raw, err := w.GetObject(ctx, WorkerArtifactManifestKey(out.Artifact))
		if err != nil {
			return nil, err
		}
		if workerartifact.BytesDigest(raw) != out.Artifact.ManifestDigest {
			return nil, errors.New("artifact manifest digest mismatch")
		}
		var manifest workerartifact.WorkerManifest
		if err = json.Unmarshal(raw, &manifest); err != nil {
			return nil, err
		}

		cached = WorkerCachedAssets{Used: time.Now().UTC()}
		if assets := manifest.Assets; assets != nil {
			cached.Config = &domain.WorkerAssetsConfig{Directory: assets.Directory, BasePath: assets.BasePath, Headers: assets.Headers, Redirects: assets.Redirects, Binding: assets.Binding, RunWorkerFirst: assets.RunWorkerFirst, NotFoundHandling: assets.NotFoundHandling, HTMLHandling: assets.HTMLHandling}
			cached.Files = assets.Files
		}
		sort.Slice(cached.Files, func(i, j int) bool { return cached.Files[i].Path < cached.Files[j].Path })
		w.assetMu.Lock()
		if w.AssetCache == nil {
			w.AssetCache = map[string]WorkerCachedAssets{}
		}
		if len(w.AssetCache) >= 256 {
			oldest := ""
			var at time.Time
			for id, v := range w.AssetCache {
				if oldest == "" || v.Used.Before(at) {
					oldest, at = id, v.Used
				}
			}
			delete(w.AssetCache, oldest)
		}
		w.AssetCache[out.Artifact.ID] = cached
		w.assetMu.Unlock()
		return cached, nil
	})
	if e != nil {
		return out, e
	}
	manifest := value.(WorkerCachedAssets)
	out.Config, out.Files = manifest.Config, manifest.Files
	return out, nil
}
