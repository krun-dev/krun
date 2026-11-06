package control

import (
	"github.com/krun-dev/krun/internal/domain"
	workerapp "github.com/krun-dev/krun/internal/workers"
	"net/http"
)

func (s *Server) workerVersionUpload(st *State, p Principal, r *http.Request) (any, int, error) {
	var in workerapp.WorkerVersionUploadInput
	if err := body(r, &in); err != nil {
		return nil, 0, err
	}
	out, result, err := s.workerApplication(r).WorkerVersionUpload(r.Context(), st, p, workerRef(r), in)
	return out, workerHTTPStatus(result), err
}
func (s *Server) workerVersionList(st *State, p Principal, r *http.Request) (any, int, error) {
	query, err := workerListOptions(r)
	if err != nil {
		return nil, 0, err
	}
	ref := workerRef(r)
	ref.Environment = r.URL.Query().Get("environment")
	out, result, err := s.workerApplication(r).WorkerVersionList(r.Context(), st, p, ref, workerapp.VersionPageOptions{Limit: query.limit, After: query.after, Search: query.search})
	return out, workerHTTPStatus(result), err
}
func (s *Server) workerDeploymentList(st *State, p Principal, r *http.Request) (any, int, error) {
	ref := workerRef(r)
	ref.Item = r.PathValue("deployment")
	if ref.Item != "" {
		out, result, err := s.workerApplication(r).WorkerDeployments(r.Context(), st, p, ref)
		return out, workerHTTPStatus(result), err
	}
	query, err := workerListOptions(r)
	if err != nil {
		return nil, 0, err
	}
	ref.Environment = r.URL.Query().Get("environment")
	out, result, err := s.workerApplication(r).WorkerDeployments(r.Context(), st, p, ref, workerapp.VersionPageOptions{Limit: query.limit, After: query.after, Search: query.search})
	return out, workerHTTPStatus(result), err
}

func (s *Server) workerPreviewBase(st *State, p Principal, r *http.Request) (any, int, error) {
	var input *workerapp.WorkerEnvironmentUpdateInput
	if r.Method == http.MethodPatch {
		input = &workerapp.WorkerEnvironmentUpdateInput{}
		if err := body(r, input); err != nil {
			return nil, 0, err
		}
	}
	out, result, err := s.workerApplication(r).PreviewBase(r.Context(), st, p, workerRef(r), input)
	return out, workerHTTPStatus(result), err
}
func (s *Server) workerSecretBatch(st *State, p Principal, r *http.Request) (any, int, error) {
	environment := r.PathValue("environment")
	authEnv := environment
	if environment == "" {
		environment = domain.PreviewBaseName
		authEnv = "global"
	}
	target, err := s.workerTarget(st, r, authEnv, "worker.publish", p)
	if err != nil {
		return nil, 0, err
	}
	var input workerapp.WorkerSecretBatchInput
	if err := body(r, &input); err != nil {
		return nil, 0, err
	}
	out, result, err := s.workerApplication(r).WorkerSecretBatch(r.Context(), st, p, workerRef(r), target, environment, input)
	return out, workerHTTPStatus(result), err
}
