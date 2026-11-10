package control

import (
	"context"
	"net/http"
	"strings"

	"github.com/krun-dev/krun/internal/domain"
	workerapp "github.com/krun-dev/krun/internal/workers"
)

func (s *Server) cfHyperdrive(st *State, p Principal, r *http.Request) (any, int, error) {
	return s.hyperdriveRequest(st, p, r, true)
}

func (s *Server) workerHyperdrive(st *State, p Principal, r *http.Request) (any, int, error) {
	return s.hyperdriveRequest(st, p, r, false)
}

func (s *Server) hyperdriveRequest(st *State, p Principal, r *http.Request, cloudflare bool) (any, int, error) {
	app := s.workerApplication(r)
	org := r.PathValue("org")
	id := r.PathValue("hyperdrive")
	if err := app.AuthorizeHyperdrive(st, p, org, r.Method != http.MethodGet); err != nil {
		return nil, 0, err
	}
	var input workerapp.HyperdriveInput
	if r.Method == http.MethodPost || r.Method == http.MethodPatch {
		// Wrangler submits JSON as a string, which Fetch marks text/plain.
		// Keep the relaxed media type on the token-authenticated adapter only.
		if cloudflare && strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			media := r.Header.Get("Content-Type")
			if media == "" || strings.HasPrefix(media, "text/plain") {
				r = r.Clone(r.Context())
				r.Header.Set("Content-Type", "application/json")
			}
		}
		if err := cfBody(r, &input); err != nil {
			return nil, 0, err
		}
	}
	return deferredResponse{run: func(ctx context.Context) (any, int, error) {
		switch r.Method {
		case http.MethodGet:
			if id != "" {
				item, err := s.Store.GetHyperdrive(ctx, org, id)
				if cloudflare {
					return cfHyperdriveConfig(item), 200, err
				}
				return item, 200, err
			}
			items, err := s.Store.ListHyperdrive(ctx, org)
			if !cloudflare {
				return map[string]any{"items": items}, 200, err
			}
			out := make([]any, 0, len(items))
			for _, item := range items {
				out = append(out, cfHyperdriveConfig(item))
			}
			return out, 200, err
		case http.MethodPost, http.MethodPatch:
			item, err := app.SaveHyperdrive(ctx, org, p.User.ID, id, input)
			if cloudflare {
				return cfHyperdriveConfig(item), 200, err
			}
			return item, 200, err
		case http.MethodDelete:
			return nil, 200, s.Store.DeleteHyperdrive(ctx, org, id)
		default:
			return nil, 0, notFound()
		}
	}}, 200, nil
}

func cfHyperdriveConfig(config domain.HyperdriveConfig) map[string]any {
	return map[string]any{
		"id": config.ID, "name": config.Name,
		"origin": map[string]any{
			"scheme": "postgresql", "host": config.Host, "port": config.Port,
			"database": config.Database, "user": config.User,
		},
		"caching": map[string]bool{"disabled": true},
	}
}

func (s *Server) workerRuntimeHyperdrive(w http.ResponseWriter, r *http.Request) {
	pool, allowed := s.workerRuntimeServingPool(r)
	if !allowed || (pool.PublicEgress != "direct" && pool.PrivateEgress != "direct") {
		s.writeError(w, r, forbidden())
		return
	}
	version, _, err := s.Store.RuntimeVersion(r.Context(), r.PathValue("version"), pool.ID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	bindings, err := s.workerApplication(nil).HyperdriveConnectionStrings(version)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.json(w, 200, bindings)
}
