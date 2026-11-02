package control

import (
	"net/http"

	repositories "github.com/krun-dev/krun/internal/repository"
	workerapp "github.com/krun-dev/krun/internal/workers"
)

func (s *Server) repositoryApplication() *repositories.Service {
	return &repositories.Service{Config: &s.Config, Store: s.Store}
}
func (s *Server) workerApplication(r *http.Request) *workerapp.Service {
	service := &workerapp.Service{Config: &s.Config, Store: s.Store, Controller: s.workers, Repository: s.repositoryApplication()}
	if r != nil {
		service.Require = func(st *State, p Principal, action string, target Target) error {
			return s.require(st, p, r, action, target)
		}
		service.Audit = func(st *State, p Principal, action, target string, allowed bool) {
			s.audit(st, p, r, action, target, allowed)
		}
	}
	return service
}
func workerRef(r *http.Request) workerapp.ServiceRef {
	ref := workerapp.ServiceRef{Organization: r.PathValue("org"), Service: r.PathValue("resource"), Environment: r.PathValue("environment")}
	for _, name := range []string{"build", "operation", "artifact", "release", "secret"} {
		if id := r.PathValue(name); id != "" {
			ref.Item = id
			break
		}
	}
	return ref
}
func workerHTTPStatus(outcome workerapp.Outcome) int {
	switch outcome {
	case workerapp.Created:
		return http.StatusCreated
	case workerapp.Queued:
		return http.StatusAccepted
	case workerapp.Deleted:
		return http.StatusNoContent
	default:
		return http.StatusOK
	}
}

const workerBuildRequestBodyMax = workerapp.WorkerBuildRequestBodyMax

var workerSecretNameValid = workerapp.WorkerSecretNameValid
