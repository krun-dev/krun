package control

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
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
	latest, ok, e := s.Store.LatestConfigRevision(r.Context())
	if e != nil {
		s.writeError(w, r, e)
		return
	}
	if !ok {
		s.json(w, 200, map[string]any{"pool": pool.ID, "config_revision": 0, "routes": []workerConfigRoute{}, "releases": map[string]WorkerRelease{}})
		return
	}
	var projection workerConfigProjection
	if e := json.Unmarshal(latest.Payload, &projection); e != nil {
		s.writeError(w, r, e)
		return
	}
	projection = workerConfigForPool(projection, pool.ID)
	s.json(w, 200, map[string]any{"pool": pool.ID, "config_revision": latest.Revision, "routes": projection.Routes, "releases": projection.Releases})
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
		revisions, floor, err := s.Store.ConfigRevisionsSince(r.Context(), since, 100)
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
	var out WorkerArtifact
	// The liveness check below consults krun_resources, which lives in the IAM
	// partition, so this read needs both halves.
	e := s.Store.View(r.Context(), PartitionAll, func(st *State) error {
		a, ok := st.Workers.Artifacts[r.PathValue("artifact")]
		if !ok || a.State != "finalized" {
			return notFound()
		}
		allowed := false
		for _, d := range st.Workers.Releases {
			if d.Artifact == a.ID && workerServiceAvailableInPool(st.Workers, d.Service, pool.ID) {
				if _, ok := st.Resources[d.Service]; ok {
					allowed = true
				}
			}
		}
		if !allowed {
			return forbidden()
		}
		out = a
		return nil
	})
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
	digest, encrypted, err := s.Store.encryptedReleaseSecrets(r.Context(), r.PathValue("release"), pool.ID)
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
	// st.Resources is in the IAM partition; without it every release looks dead.
	e := w.server.Store.View(ctx, PartitionAll, func(st *State) error {
		rel, ok := st.Workers.Releases[releaseID]
		if !ok || !workerServiceAvailableInPool(st.Workers, rel.Service, pool) || st.Resources[rel.Service].ID == "" {
			return notFound()
		}
		art, ok := st.Workers.Artifacts[rel.Artifact]
		if !ok || art.State != "finalized" {
			return notFound()
		}
		out.Release, out.Artifact = rel, art
		return nil
	})
	if e != nil {
		return out, e
	}
	w.assetMu.RLock()
	cached, ok := w.assetCache[out.Artifact.ID]
	w.assetMu.RUnlock()
	if ok {
		out.Config, out.Files = cached.Config, cached.Files
		return out, nil
	}
	code, e := w.artifactCode(ctx, out.Artifact)
	if e != nil {
		return out, e
	}
	manifest := workerCachedAssets{Config: code.Assets}
	for name, meta := range code.AssetFiles {
		manifest.Files = append(manifest.Files, workerManifestFile{Path: name, Digest: meta.Digest, Size: meta.Size, ContentType: meta.ContentType})
	}
	sort.Slice(manifest.Files, func(i, j int) bool { return manifest.Files[i].Path < manifest.Files[j].Path })
	w.assetMu.Lock()
	if len(w.assetCache) >= 256 {
		w.assetCache = map[string]workerCachedAssets{}
	}
	w.assetCache[out.Artifact.ID] = manifest
	w.assetMu.Unlock()
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
	signed, e := s.workers.runtimeS3().PresignedGetObject(r.Context(), s.workers.config.S3Bucket, key, 15*time.Minute, nil)
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
