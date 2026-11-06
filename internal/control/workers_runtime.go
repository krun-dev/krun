package control

import (
	"crypto/subtle"
	"net/http"
	"strconv"
	"time"

	"github.com/krun-dev/krun/internal/adapters/runtimeclient"
	appconfig "github.com/krun-dev/krun/internal/config"
)

func (s *Server) workerRuntimePool(r *http.Request) (appconfig.WorkerRuntimePool, bool) {
	if s.Config.Workers == nil {
		return appconfig.WorkerRuntimePool{}, false
	}
	pool, ok := s.Config.Workers.RuntimePool(r.Header.Get("X-Runtime-Pool"))
	serving := subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+pool.RuntimeToken))
	validator := 0
	if pool.ValidatorToken != "" {
		validator = subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+pool.ValidatorToken))
	}
	if !ok || serving|validator != 1 {
		return appconfig.WorkerRuntimePool{}, false
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
	s.json(w, 200, map[string]any{"pool": pool.ID, "config_revision": head, "lazy_routes": true, "routes": []workerConfigRoute{}, "versions": map[string]WorkerVersion{}})
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
		changes, err := s.runtimeApplication().ReadChanges(r.Context(), pool.ID, since)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		if changes.Resync {
			s.workerConfigResync(w, r)
			return
		}
		if changes.Batch != nil {
			s.json(w, 200, changes.Batch)
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
	pool, allowed := s.workerRuntimeServingPool(r)
	if !allowed {
		s.writeError(w, r, forbidden())
		return
	}
	out, err := s.runtimeApplication().Artifact(r.Context(), r.PathValue("artifact"), pool.ID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	s.json(w, 200, map[string]any{
		"artifact_id":    out.ID,
		"object_key":     out.ObjectKey,
		"archive_sha256": out.ArchiveDigest,
		"module_sha256":  out.ModuleDigest,
		"size":           out.Size,
		"storage":        runtimeclient.ReadGrantPayload(out.Storage),
	})
}

// workerRuntimeVersionSecrets is the only plaintext delivery path. It is
// available solely on the internal listener, requires the runtime credential,
// and returns values for one immutable Version. Values are never cached by the
// control plane or included in edge snapshots.
func (s *Server) workerRuntimeVersionSecrets(w http.ResponseWriter, r *http.Request) {
	pool, allowed := s.workerRuntimePool(r)
	if !allowed {
		s.writeError(w, r, forbidden())
		return
	}
	out, err := s.runtimeApplication().Secrets(r.Context(), r.PathValue("version"), pool.ID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.json(w, 200, out)
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
	if err := s.runtimeApplication().Ack(r.Context(), pool.ID, in); err != nil {
		s.writeError(w, r, err)
		return
	}
	s.json(w, 200, map[string]bool{"ok": true})
}

func (s *Server) workerRuntimeVersionAssets(w http.ResponseWriter, r *http.Request) {
	pool, allowed := s.workerRuntimeServingPool(r)
	if !allowed {
		s.writeError(w, r, forbidden())
		return
	}
	out, err := s.runtimeApplication().Assets(r.Context(), r.PathValue("version"), pool.ID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var grant any
	if out.Storage != nil {
		grant = runtimeclient.ReadGrantPayload(*out.Storage)
	}
	w.Header().Set("Cache-Control", "no-store")
	s.json(w, 200, map[string]any{
		"version_id": out.Version,
		"config":     out.Config,
		"files":      out.Files,
		"storage":    grant,
	})
}

func (s *Server) workerRuntimeAssetCredentials(w http.ResponseWriter, r *http.Request) {
	pool, allowed := s.workerRuntimeServingPool(r)
	if !allowed {
		s.writeError(w, r, forbidden())
		return
	}
	grant, err := s.runtimeApplication().AssetCredentials(r.Context(), r.PathValue("version"), pool.ID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	s.json(w, 200, runtimeclient.ReadGrantPayload(grant))
}

// A cold host resolves exactly one normalized route and immutable version.
func (s *Server) workerRuntimeConfigRoute(w http.ResponseWriter, r *http.Request) {
	pool, allowed := s.workerRuntimePool(r)
	if !allowed {
		s.writeError(w, r, forbidden())
		return
	}
	out, err := s.runtimeApplication().Route(r.Context(), r.PathValue("host"), pool.ID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.json(w, 200, out)
}

func (s *Server) workerRuntimeStaticAssetCredentials(w http.ResponseWriter, r *http.Request) {
	pool, allowed := s.workerRuntimeServingPool(r)
	if !allowed {
		s.writeError(w, r, forbidden())
		return
	}
	grant, err := s.runtimeApplication().StaticAssetCredentials(
		r.Context(), r.PathValue("organization"), r.PathValue("worker"),
		r.PathValue("digest"), r.PathValue("prefix"), pool,
	)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	s.json(w, 200, runtimeclient.ReadGrantPayload(grant))
}

func (s *Server) workerRuntimeServingPool(r *http.Request) (appconfig.WorkerRuntimePool, bool) {
	pool, allowed := s.workerRuntimePool(r)
	serving := subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+pool.RuntimeToken))
	return pool, allowed && serving == 1
}
