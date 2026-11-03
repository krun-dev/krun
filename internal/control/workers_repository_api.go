package control

import (
	"net/http"

	workerapp "github.com/krun-dev/krun/internal/workers"
)

func (s *Server) workerNetworkProfiles(st *State, p Principal, r *http.Request) (any, int, error) {
	out, outcome, err := s.workerApplication(r).WorkerNetworkProfiles(r.Context(), st, p, workerRef(r))
	return out, workerHTTPStatus(outcome), err
}

// workerDomains lists the domain catalog so the console can offer choices.
func (s *Server) workerDomains(st *State, p Principal, r *http.Request) (any, int, error) {
	out, outcome, err := s.workerApplication(r).WorkerDomains(r.Context(), st, p, workerRef(r))
	return out, workerHTTPStatus(outcome), err
}

func (s *Server) workerServiceList(st *State, p Principal, r *http.Request) (any, int, error) {
	out, _, err := s.workerApplication(r).WorkerServiceList(r.Context(), st, p, workerRef(r))
	if err != nil {
		return nil, 0, err
	}
	return resourcePageItems(st, r, out)
}

func (s *Server) workerRepositoryProviders(st *State, p Principal, r *http.Request) (any, int, error) {
	out, outcome, err := s.workerApplication(r).WorkerRepositoryProviders(r.Context(), st, p, workerRef(r))
	return out, workerHTTPStatus(outcome), err
}

func (s *Server) workerRepositoryGet(st *State, p Principal, r *http.Request) (any, int, error) {
	out, outcome, err := s.workerApplication(r).WorkerRepositoryGet(r.Context(), st, p, workerRef(r))
	return out, workerHTTPStatus(outcome), err
}

func (s *Server) workerRepositoryUpdate(st *State, p Principal, r *http.Request) (any, int, error) {
	// Repository settings are shared by all environments of a service.
	target, err := s.workerTarget(st, r, "global", "service.update", p)
	if err != nil {
		return nil, 0, err
	}
	var in workerapp.WorkerRepositoryUpdateInput
	if err := body(r, &in); err != nil {
		return nil, 0, err
	}
	out, outcome, err := s.workerApplication(r).WorkerRepositoryUpdate(r.Context(), st, p, workerRef(r), target, in)
	return out, workerHTTPStatus(outcome), err
}
