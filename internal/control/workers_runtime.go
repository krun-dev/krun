package control

import (
	"crypto/subtle"
	"net/http"
	"strconv"
	"time"

	appconfig "github.com/krun-dev/krun/internal/config"
)

func (s *Server) workerRuntimePool(r *http.Request) (appconfig.WorkerRuntimePool, bool) {
	if s.Config.Workers == nil {
		return appconfig.WorkerRuntimePool{}, false
	}
	pool, ok := s.Config.Workers.RuntimePool(r.Header.Get("X-Runtime-Pool"))
	if !ok || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+pool.RuntimeToken)) != 1 {
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
	pool, allowed := s.workerRuntimePool(r)
	if !allowed {
		s.writeError(w, r, forbidden())
		return
	}
	out, err := s.runtimeApplication().Artifact(r.Context(), r.PathValue("artifact"), pool.ID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.json(w, 200, out)
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
	pool, allowed := s.workerRuntimePool(r)
	if !allowed {
		s.writeError(w, r, forbidden())
		return
	}
	out, err := s.runtimeApplication().Assets(r.Context(), r.PathValue("version"), pool.ID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.json(w, 200, out)
}

func (s *Server) workerRuntimeVersionAssetURL(w http.ResponseWriter, r *http.Request) {
	pool, allowed := s.workerRuntimePool(r)
	if !allowed {
		s.writeError(w, r, forbidden())
		return
	}
	out, err := s.runtimeApplication().AssetURL(r.Context(), r.PathValue("version"), r.PathValue("path"), pool)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.json(w, 200, out)
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

func (s *Server) workerRuntimeStaticAssetURL(w http.ResponseWriter, r *http.Request) {
	pool, ok := s.workerRuntimePool(r)
	if !ok {
		s.writeError(w, r, forbidden())
		return
	}
	out, err := s.runtimeApplication().StaticAssetURL(
		r.Context(),
		r.PathValue("organization"),
		r.PathValue("service"),
		r.PathValue("digest"),
		r.PathValue("path"),
		r.PathValue("prefix"),
		pool,
	)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.json(w, 200, out)
}
