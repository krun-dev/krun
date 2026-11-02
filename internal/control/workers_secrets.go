package control

import (
	"net/http"

	appconfig "github.com/krun-dev/krun/internal/config"
	workerapp "github.com/krun-dev/krun/internal/workers"
)

func (s *Server) workerSecretList(st *State, p Principal, r *http.Request) (any, int, error) {
	out, outcome, err := s.workerApplication(r).WorkerSecretList(r.Context(), st, p, workerRef(r))
	return out, workerHTTPStatus(outcome), err
}

func (s *Server) workerSecretPut(st *State, p Principal, r *http.Request) (any, int, error) {
	return s.workerSecretChange(st, p, r, false)
}

func (s *Server) workerSecretDelete(st *State, p Principal, r *http.Request) (any, int, error) {
	return s.workerSecretChange(st, p, r, true)
}

// workerSecretChange mirrors Cloudflare's secret put/delete behavior: create
// an immutable Worker version and immediately deploy it. The current Artifact
// is reused, so rotating a secret never rebuilds user code.
func (s *Server) workerSecretChange(st *State, p Principal, r *http.Request, deleting bool) (any, int, error) {
	environment, name := r.PathValue("environment"), r.PathValue("secret")
	if !appconfig.WorkerEnvValid(environment) || !workerSecretNameValid(name) {
		return nil, 0, notFound()
	}
	target, err := s.workerTarget(st, r, environment, "service.publish", p)
	if err != nil {
		return nil, 0, err
	}
	var input workerapp.WorkerSecretInput
	if err = body(r, &input); err != nil {
		return nil, 0, err
	}
	out, outcome, err := s.workerApplication(r).WorkerSecretChange(r.Context(), st, p, workerRef(r), target, environment, name, deleting, input)
	return out, workerHTTPStatus(outcome), err
}
