package control

import (
	"net/http"

	workerapp "github.com/krun-dev/krun/internal/workers"
)

func (s *Server) workerConfigurationList(st *State, p Principal, r *http.Request) (any, int, error) {
	out, result, err := s.workerApplication(r).WorkerConfigurationList(r.Context(), st, p, workerRef(r))
	return out, workerHTTPStatus(result), err
}

func (s *Server) workerConfigurationCreate(st *State, p Principal, r *http.Request) (any, int, error) {
	var in workerapp.WorkerConfigurationCreateInput
	if err := body(r, &in); err != nil {
		return nil, 0, err
	}
	out, result, err := s.workerApplication(r).WorkerConfigurationCreate(r.Context(), st, p, workerRef(r), in)
	return out, workerHTTPStatus(result), err
}

func (s *Server) workerConfigurationDeploy(st *State, p Principal, r *http.Request) (any, int, error) {
	var in workerapp.WorkerConfigurationDeployInput
	if err := body(r, &in); err != nil {
		return nil, 0, err
	}
	ref := workerRef(r)
	ref.Item = r.PathValue("configuration")
	out, result, err := s.workerApplication(r).WorkerConfigurationDeploy(r.Context(), st, p, ref, in)
	return out, workerHTTPStatus(result), err
}
