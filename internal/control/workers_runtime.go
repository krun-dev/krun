package control

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

func (s *Server) workerRuntimePool(r *http.Request) (WorkerRuntimePool, bool) {
	if s.Config.Workers == nil {
		return WorkerRuntimePool{}, false
	}
	pool, ok := s.Config.Workers.runtimePool(r.Header.Get("X-Runtime-Pool"))
	if !ok || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+pool.RuntimeToken)) != 1 {
		return WorkerRuntimePool{}, false
	}
	return pool, true
}

func (s *Server) workerRuntimeAllowed(r *http.Request) bool {
	_, ok := s.workerRuntimePool(r)
	return ok
}

// workerRuntimeConfigSnapshot is the bootstrap fallback. Runtimes normally read
// the immutable snapshot from object storage; this endpoint keeps a new Pod
// bootstrappable when object storage is unreachable but the control plane is up.
func (s *Server) workerRuntimeConfigSnapshot(w http.ResponseWriter, r *http.Request) {
	pool, allowed := s.workerRuntimePool(r)
	if !allowed {
		s.writeError(w, r, forbidden())
		return
	}
	head, _, err := s.Store.ConfigState(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.json(w, 200, map[string]any{"pool": pool.ID, "config_revision": head, "lazy_routes": true, "routes": []workerConfigRoute{}, "releases": map[string]WorkerRelease{}})
}

// workerRuntimeConfigChanges is the incremental feed. since=0 means bootstrap
// from the snapshot. Responses: 200 batch, 204 timeout, 409 resync.
func (s *Server) workerRuntimeConfigChanges(w http.ResponseWriter, r *http.Request) {
	pool, allowed := s.workerRuntimePool(r)
	if !allowed {
		s.writeError(w, r, forbidden())
		return
	}
	since, e := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	if e != nil || since < 0 {
		s.writeError(w, r, problem(422, "CONFIG_SINCE_INVALID", "since must be a non-negative revision"))
		return
	}
	wait := 5 * time.Second
	if raw := r.URL.Query().Get("wait"); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil && parsed > 0 && parsed <= 25*time.Second {
			wait = parsed
		}
	}
	deadline := time.Now().Add(wait)
	for {
		revisions, floor, err := s.Store.ConfigRevisionsSince(context.WithValue(r.Context(), workerRuntimeFeedPoolKey{}, pool.ID), since, 100)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		if since > 0 && since < floor {
			s.workerConfigResync(w, r)
			return
		}
		if len(revisions) > 0 {
			if since == 0 || revisions[0].Revision != since+1 {
				s.workerConfigResync(w, r)
				return
			}
			ops := []workerConfigOp{}
			for _, revision := range revisions {
				batch := []workerConfigOp{}
				if err := json.Unmarshal(revision.Ops, &batch); err != nil {
					s.writeError(w, r, err)
					return
				}
				ops = append(ops, workerConfigOpsForPool(batch, pool.ID)...)
			}
			if len(ops) > 2000 {
				s.workerConfigResync(w, r)
				return
			}
			s.json(w, 200, map[string]any{"from": since, "to": revisions[len(revisions)-1].Revision, "ops": ops})
			return
		}
		if time.Now().After(deadline) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		notify := s.Store.ConfigNotify()
		tick := time.Second
		if remaining := time.Until(deadline); remaining < tick {
			tick = remaining
		}
		if tick <= 0 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		select {
		case <-notify:
		case <-time.After(tick):
		case <-r.Context().Done():
			return
		}
	}
}

func (s *Server) workerConfigResync(w http.ResponseWriter, r *http.Request) {
	latest, _, e := s.Store.ConfigState(r.Context())
	if e != nil {
		s.writeError(w, r, e)
		return
	}
	s.json(w, 409, map[string]any{"code": "RESYNC", "config_revision": latest})
}

func (s *Server) workerRuntimeArtifact(w http.ResponseWriter, r *http.Request) {
	pool, allowed := s.workerRuntimePool(r)
	if !allowed {
		s.writeError(w, r, forbidden())
		return
	}
	out, e := s.Store.RuntimeArtifact(r.Context(), r.PathValue("artifact"), pool.ID)

	if e != nil {
		s.writeError(w, r, e)
		return
	}
	// Runtimes fetch the object directly from storage; the control plane only
	// hands out a short-lived URL and never proxies artifact bytes.
	signed, e := s.workers.downloadURL(r.Context(), out)
	if e != nil {
		s.writeError(w, r, problem(503, "ARTIFACT_UNAVAILABLE", "Artifact storage unavailable"))
		return
	}
	s.json(w, 200, map[string]any{
		"artifact_id":    out.ID,
		"object_key":     out.ObjectKey,
		"download_url":   signed,
		"archive_sha256": out.ArchiveDigest,
		"module_sha256":  out.ModuleDigest,
		"size":           out.Size,
	})
}

// workerRuntimeReleaseSecrets is the only plaintext delivery path. It is
// available solely on the internal listener, requires the runtime credential,
// and returns values for one immutable Release. Values are never cached by the
// control plane or included in edge snapshots.
func (s *Server) workerRuntimeReleaseSecrets(w http.ResponseWriter, r *http.Request) {
	pool, allowed := s.workerRuntimePool(r)
	if !allowed {
		s.writeError(w, r, forbidden())
		return
	}
	digest, encrypted, err := s.Store.EncryptedReleaseSecrets(r.Context(), r.PathValue("release"), pool.ID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	values := make(map[string]string, len(encrypted))
	versions := make(map[string]string, len(encrypted))
	for _, item := range encrypted {
		value, decryptErr := decryptWorkerSecret(s.Config.Workers, item)
		if decryptErr != nil {
			s.writeError(w, r, problem(503, "WORKER_SECRET_UNAVAILABLE", "Worker secret unavailable"))
			return
		}
		values[item.Version.Name] = value
		versions[item.Version.Name] = item.Version.ID
	}
	if workerSecretsDigest(versions) != digest {
		s.writeError(w, r, problem(503, "WORKER_SECRET_UNAVAILABLE", "Worker secret binding mismatch"))
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	s.json(w, 200, map[string]any{"release_id": r.PathValue("release"), "secret_digest": digest, "values": values})
}
func (s *Server) workerRuntimeAck(w http.ResponseWriter, r *http.Request) {
	pool, allowed := s.workerRuntimePool(r)
	if !allowed {
		s.writeError(w, r, forbidden())
		return
	}
	var in WorkerAck
	if e := body(r, &in); e != nil {
		s.writeError(w, r, e)
		return
	}
	if in.Pool != pool.ID || in.Pod == "" || in.UID == "" {
		s.writeError(w, r, problem(422, "ACK_INVALID", "Pod UID and pool required"))
		return
	}
	head, _, e := s.Store.ConfigState(r.Context())
	if e != nil {
		s.writeError(w, r, e)
		return
	}
	if in.Revision < 0 || int64(in.Revision) > head {
		s.writeError(w, r, problem(422, "ACK_INVALID", "Config revision is outside the published range"))
		return
	}
	in.Time = now()
	if e := s.Store.RecordAck(r.Context(), in); e != nil {
		s.writeError(w, r, e)
		return
	}
	s.json(w, 200, map[string]bool{"ok": true})
}

type workerReleaseAssets struct {
	Release  WorkerRelease
	Artifact WorkerArtifact
	Config   *WorkerAssetsConfig
	Files    []workerManifestFile
}

func (w *workersController) releaseAssets(ctx context.Context, releaseID, pool string) (workerReleaseAssets, error) {
	var out workerReleaseAssets
	rel, art, e := w.server.Store.RuntimeRelease(ctx, releaseID, pool)
	out.Release, out.Artifact = rel, art

	if e != nil {
		return out, e
	}
	value, e, _ := w.assetFlights.Do(out.Artifact.ID, func() (any, error) {
		w.assetMu.Lock()
		cached, ok := w.assetCache[out.Artifact.ID]
		if ok {
			cached.Used = now()
			w.assetCache[out.Artifact.ID] = cached
		}
		w.assetMu.Unlock()
		if ok {
			return cached, nil
		}
		raw, err := w.getObject(ctx, workerArtifactManifestKey(out.Artifact))
		if err != nil {
			return nil, err
		}
		if bytesDigest(raw) != out.Artifact.ManifestDigest {
			return nil, errors.New("artifact manifest digest mismatch")
		}
		var manifest workerManifest
		if err = json.Unmarshal(raw, &manifest); err != nil {
			return nil, err
		}

		cached = workerCachedAssets{Used: now()}
		if assets := manifest.Assets; assets != nil {
			cached.Config = &WorkerAssetsConfig{Directory: assets.Directory, Binding: assets.Binding, RunWorkerFirst: assets.RunWorkerFirst, NotFoundHandling: assets.NotFoundHandling, HTMLHandling: assets.HTMLHandling}
			cached.Files = assets.Files
		}
		sort.Slice(cached.Files, func(i, j int) bool { return cached.Files[i].Path < cached.Files[j].Path })
		w.assetMu.Lock()
		if w.assetCache == nil {
			w.assetCache = map[string]workerCachedAssets{}
		}
		if len(w.assetCache) >= 256 {
			oldest := ""
			var at time.Time
			for id, v := range w.assetCache {
				if oldest == "" || v.Used.Before(at) {
					oldest, at = id, v.Used
				}
			}
			delete(w.assetCache, oldest)
		}
		w.assetCache[out.Artifact.ID] = cached
		w.assetMu.Unlock()
		return cached, nil
	})
	if e != nil {
		return out, e
	}
	manifest := value.(workerCachedAssets)
	out.Config, out.Files = manifest.Config, manifest.Files
	return out, nil
}

func (s *Server) workerRuntimeReleaseAssets(w http.ResponseWriter, r *http.Request) {
	pool, allowed := s.workerRuntimePool(r)
	if !allowed {
		s.writeError(w, r, forbidden())
		return
	}
	assets, e := s.workers.releaseAssets(r.Context(), r.PathValue("release"), pool.ID)
	if e != nil {
		s.writeError(w, r, e)
		return
	}
	s.json(w, 200, map[string]any{"release_id": assets.Release.ID, "config": assets.Config, "files": assets.Files})
}

func (s *Server) workerRuntimeReleaseAssetURL(w http.ResponseWriter, r *http.Request) {
	pool, allowed := s.workerRuntimePool(r)
	if !allowed {
		s.writeError(w, r, forbidden())
		return
	}
	assets, e := s.workers.releaseAssets(r.Context(), r.PathValue("release"), pool.ID)
	if e != nil {
		s.writeError(w, r, e)
		return
	}
	name := r.PathValue("path")
	var file *workerManifestFile
	for i := range assets.Files {
		if assets.Files[i].Path == name {
			file = &assets.Files[i]
			break
		}
	}
	if file == nil {
		s.writeError(w, r, notFound())
		return
	}
	prefix := "int"
	if runtimePoolHasAccess(pool, "public") {
		prefix = "pub"
	}
	key := workerAssetObjectKey(prefix, assets.Release.Organization, assets.Release.Service, assets.Artifact.ArchiveDigest, name)
	signed, e := s.workers.s3Runtime.PresignedGetObject(r.Context(), s.workers.config.S3Bucket, key, 15*time.Minute, nil)
	if e != nil {
		s.writeError(w, r, problem(503, "ASSET_UNAVAILABLE", "Asset storage unavailable"))
		return
	}
	s.json(w, 200, map[string]any{
		"path":         file.Path,
		"sha256":       file.Digest,
		"size_bytes":   file.Size,
		"content_type": file.ContentType,
		"download_url": signed.String(),
	})
}

// A cold host resolves exactly one normalized route and immutable release.
func (s *Server) workerRuntimeConfigRoute(w http.ResponseWriter, r *http.Request) {
	pool, ok := s.workerRuntimePool(r)
	if !ok {
		s.writeError(w, r, forbidden())
		return
	}
	host := strings.ToLower(r.PathValue("host"))
	if len(host) > 253 || strings.ContainsAny(host, "/\\: ") {
		s.writeError(w, r, notFound())
		return
	}
	projection, revision, err := s.Store.RuntimeConfigRoute(r.Context(), host, pool.ID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.json(w, 200, workerConfigSnapshot{Pool: pool.ID, ConfigRevision: revision, Routes: projection.Routes, Releases: projection.Releases})
}
