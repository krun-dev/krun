package control

import (
	"context"
	"crypto/subtle"
	"net/http"
	"sort"
	"time"
)

func (s *Server) workerRuntimeAllowed(r *http.Request) bool {
	return s.Config.Workers != nil && r.Header.Get("X-Runtime-Pool") == "default" && subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+s.Config.Workers.RuntimeToken)) == 1
}
func (s *Server) workerRuntimeError(w http.ResponseWriter, r *http.Request, e error) {
	s.writeError(w, r, e)
}
func (s *Server) workerRuntimeSnapshot(w http.ResponseWriter, r *http.Request) {
	if !s.workerRuntimeAllowed(r) {
		s.writeError(w, r, forbidden())
		return
	}
	var out any
	e := s.Store.Update(r.Context(), func(st *State) error {
		routes := []map[string]any{}
		releases := map[string]WorkerRelease{}
		for id, ws := range st.Workers.Services {
			resource, ok := st.Resources[id]
			if !ok || resource.Kind != "service" || resource.Organization != ws.Organization {
				continue
			}
			if _, ok := st.Organizations[ws.Organization]; !ok {
				continue
			}
			for _, env := range ws.Environments {
				routes = append(routes, map[string]any{"host": env.Host, "path": "/", "scopes": []string{"private"}, "enabled": env.Enabled, "release": env.Release})
			}
		}
		for id, d := range st.Workers.Releases {
			if _, ok := st.Resources[d.Service]; ok && d.Pool == "default" {
				releases[id] = d
			}
		}
		out = map[string]any{"pool": "default", "revision": st.Workers.Revision, "validUntil": now().Add(5 * time.Second).UnixMilli(), "routes": routes, "releases": releases}
		return nil
	})
	if e != nil {
		s.writeError(w, r, e)
		return
	}
	s.json(w, 200, out)
}
func (s *Server) workerRuntimeRelease(w http.ResponseWriter, r *http.Request) {
	if !s.workerRuntimeAllowed(r) {
		s.writeError(w, r, forbidden())
		return
	}
	var out WorkerRelease
	e := s.Store.Update(r.Context(), func(st *State) error {
		d, ok := st.Workers.Releases[r.PathValue("release")]
		if !ok || d.Pool != "default" {
			return notFound()
		}
		if _, ok := st.Resources[d.Service]; !ok {
			return notFound()
		}
		out = d
		return nil
	})
	if e != nil {
		s.writeError(w, r, e)
		return
	}
	s.json(w, 200, out)
}
func (s *Server) workerRuntimeArtifact(w http.ResponseWriter, r *http.Request) {	if !s.workerRuntimeAllowed(r) {
		s.writeError(w, r, forbidden())
		return
	}
	var out WorkerArtifact
	e := s.Store.Update(r.Context(), func(st *State) error {
		a, ok := st.Workers.Artifacts[r.PathValue("artifact")]
		if !ok || a.State != "finalized" {
			return notFound()
		}
		allowed := false
		for _, d := range st.Workers.Releases {
			if d.Artifact == a.ID && d.Pool == "default" {
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
func (s *Server) workerRuntimeAck(w http.ResponseWriter, r *http.Request) {
	if !s.workerRuntimeAllowed(r) {
		s.writeError(w, r, forbidden())
		return
	}
	var in WorkerAck
	if e := body(r, &in); e != nil {
		s.writeError(w, r, e)
		return
	}
	if in.Pool != "default" || in.Pod == "" || in.UID == "" {
		s.writeError(w, r, problem(422, "ACK_INVALID", "Pod UID and pool required"))
		return
	}
	e := s.Store.Update(r.Context(), func(st *State) error {
		if in.Revision != st.Workers.Revision {
			return problem(409, "REVISION_CONFLICT", "Snapshot revision changed")
		}
		in.Time = now()
		st.Workers.Acks[in.UID] = in
		for id, ack := range st.Workers.Acks {
			if now().Sub(ack.Time) > time.Minute {
				delete(st.Workers.Acks, id)
			}
		}
		return nil
	})
	if e != nil {
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

func (w *workersController) releaseAssets(ctx context.Context, releaseID string) (workerReleaseAssets, error) {
	var out workerReleaseAssets
	e := w.server.Store.Update(ctx, func(st *State) error {
		rel, ok := st.Workers.Releases[releaseID]
		if !ok || rel.Pool != "default" || st.Resources[rel.Service].ID == "" {
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
	if !s.workerRuntimeAllowed(r) {
		s.writeError(w, r, forbidden())
		return
	}
	assets, e := s.workers.releaseAssets(r.Context(), r.PathValue("release"))
	if e != nil {
		s.writeError(w, r, e)
		return
	}
	s.json(w, 200, map[string]any{"release_id": assets.Release.ID, "config": assets.Config, "files": assets.Files})
}

func (s *Server) workerRuntimeReleaseAssetURL(w http.ResponseWriter, r *http.Request) {
	if !s.workerRuntimeAllowed(r) {
		s.writeError(w, r, forbidden())
		return
	}
	assets, e := s.workers.releaseAssets(r.Context(), r.PathValue("release"))
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
	key := workerAssetObjectKey("pub", assets.Release.Organization, assets.Release.Service, assets.Artifact.ArchiveDigest, name)
	signed, e := s.workers.s3Builder.PresignedGetObject(r.Context(), s.workers.config.S3Bucket, key, 15*time.Minute, nil)
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
