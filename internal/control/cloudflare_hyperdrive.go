package control

import (
	"context"
	"net/http"

	workerapp "github.com/krun-dev/krun/internal/workers"
)

func (s *Server) workerHyperdrive(st *State, p Principal, r *http.Request) (any, int, error) {
	return s.hyperdriveRequest(st, p, r)
}

func (s *Server) hyperdriveRequest(st *State, p Principal, r *http.Request) (any, int, error) {
	app := s.workerApplication(r)
	org := r.PathValue("org")
	id := r.PathValue("hyperdrive")
	if err := app.AuthorizeHyperdrive(st, p, org, r.Method != http.MethodGet); err != nil {
		return nil, 0, err
	}
	var input workerapp.HyperdriveInput
	if r.Method == http.MethodPost || r.Method == http.MethodPatch {
		if err := cfBody(r, &input); err != nil {
			return nil, 0, err
		}
	}
	return deferredResponse{run: func(ctx context.Context) (any, int, error) {
		switch r.Method {
		case http.MethodGet:
			if id != "" {
				item, err := s.Store.GetHyperdrive(ctx, org, id)
				return item, 200, err
			}
			items, err := s.Store.ListHyperdrive(ctx, org)
			return map[string]any{"items": items}, 200, err
		case http.MethodPost, http.MethodPatch:
			item, err := app.SaveHyperdrive(ctx, org, p.User.ID, id, input)
			return item, 200, err
		case http.MethodDelete:
			return nil, 200, s.Store.DeleteHyperdrive(ctx, org, id)
		default:
			return nil, 0, notFound()
		}
	}}, 200, nil
}

func (s *Server) workerRuntimeHyperdrive(w http.ResponseWriter, r *http.Request) {
	pool, allowed := s.workerRuntimeServingPool(r)
	if !allowed || (pool.PublicEgress != "direct" && pool.PrivateEgress != "direct") {
		s.writeError(w, r, forbidden())
		return
	}
	bindings, err := s.workerApplication(nil).HyperdriveConnectionStrings(r.Context(), r.PathValue("version"), pool.ID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	s.json(w, 200, bindings)
}
