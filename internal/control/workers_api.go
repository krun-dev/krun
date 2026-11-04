package control

import (
	"net/http"

	appconfig "github.com/krun-dev/krun/internal/config"
	workerapp "github.com/krun-dev/krun/internal/workers"
)

func (s *Server) requireWorkers(w http.ResponseWriter, r *http.Request) bool {
	if s.Config.Workers != nil {
		return true
	}
	s.writeError(w, r, problem(503, "WORKERS_NOT_CONFIGURED", "Workers is not configured"))
	return false
}

func (s *Server) workersRoutes(m *http.ServeMux) {
	base := "/api/v1/orgs/{org}/services/{resource}/workers"
	protectWithBodyLimit := func(sessionOnly bool, bodyLimit int64, fn endpoint) http.HandlerFunc {
		return s.protectedWorkersWithBodyLimit(sessionOnly, bodyLimit, func(st *State, p Principal, r *http.Request) (any, int, error) {
			if s.Config.Workers == nil {
				return nil, 0, problem(503, "WORKERS_NOT_CONFIGURED", "Workers is not configured")
			}
			return fn(st, p, r)
		})
	}
	protect := func(sessionOnly bool, fn endpoint) http.HandlerFunc {
		return protectWithBodyLimit(sessionOnly, apiJSONBodyMax, fn)
	}
	m.HandleFunc("GET /api/v1/orgs/{org}/workers/buckets", protect(false, s.workerBucketList))
	m.HandleFunc("POST /api/v1/orgs/{org}/workers/buckets", protect(false, s.workerBucketCreate))
	m.HandleFunc("PATCH /api/v1/orgs/{org}/workers/buckets/{bucket}", protect(false, s.workerBucketUpdate))
	m.HandleFunc("GET /api/v1/orgs/{org}/workers/services", protect(false, s.workerServiceList))
	m.HandleFunc("GET /api/v1/orgs/{org}/workers/domains", protect(false, s.workerDomains))
	m.HandleFunc("GET /api/v1/orgs/{org}/workers/network-profiles", protect(false, s.workerNetworkProfiles))
	m.HandleFunc("GET /api/v1/orgs/{org}/workers/repository-providers", protect(false, s.workerRepositoryProviders))
	m.HandleFunc("POST /api/v1/orgs/{org}/workers/repository/{provider}/connect", protect(true, s.workerRepositoryConnect))
	m.HandleFunc("DELETE /api/v1/orgs/{org}/workers/repository/{provider}/disconnect", protect(true, s.workerRepositoryDisconnect))
	m.HandleFunc("GET /api/v1/workers/repository/{provider}/callback", s.workerRepositoryCallback)
	m.HandleFunc("POST /api/v1/workers/repository/{provider}/callback", s.workerRepositoryCallback)
	m.HandleFunc("GET "+base+"/domains", protect(false, s.workerServiceDomains))
	m.HandleFunc("PUT "+base+"/domains", protect(false, s.workerServiceDomainsUpdate))
	m.HandleFunc("PUT "+base+"/network-profile", protect(false, s.workerServiceNetworkProfileUpdate))
	m.HandleFunc("GET "+base+"/repository", protect(false, s.workerRepositoryGet))
	m.HandleFunc("PUT "+base+"/repository", protect(false, s.workerRepositoryUpdate))
	m.HandleFunc("POST "+base+"/builds", protectWithBodyLimit(false, workerapp.WorkerBuildRequestBodyMax, s.workerBuildCreate))
	m.HandleFunc("POST "+base+"/source-uploads", protect(false, s.workerSourceUploadCreate))
	m.HandleFunc("GET "+base+"/builds", protect(false, s.workerBuildList))
	m.HandleFunc("GET "+base+"/builds/{build}", protect(false, s.workerBuildGet))
	m.HandleFunc("GET "+base+"/builds/{build}/logs", protect(false, s.workerBuildLogs))
	m.HandleFunc("GET "+base+"/builds/{build}/source", protect(false, s.workerBuildSource))
	m.HandleFunc("GET "+base+"/overview", protect(false, s.workerOverview))
	m.HandleFunc("GET "+base+"/environments", protect(false, s.workerEnvironments))
	m.HandleFunc("POST "+base+"/environments", protect(false, s.workerEnvironmentCreate))
	m.HandleFunc("DELETE "+base+"/environments/{environment}", protect(false, s.workerEnvironmentDelete))
	m.HandleFunc("PATCH "+base+"/environments/{environment}", protect(false, s.workerEnvironmentUpdate))
	m.HandleFunc("GET "+base+"/environments/{environment}/secrets", protect(false, s.workerSecretList))
	m.HandleFunc("PUT "+base+"/environments/{environment}/secrets/{secret}", protect(false, s.workerSecretPut))
	m.HandleFunc("DELETE "+base+"/environments/{environment}/secrets/{secret}", protect(false, s.workerSecretDelete))
	m.HandleFunc("POST "+base+"/environments/{environment}/deployments", protect(false, s.workerPublish))
	m.HandleFunc("POST "+base+"/environments/{environment}/rollbacks", protect(false, s.workerRollback))
	m.HandleFunc("GET "+base+"/environments/{environment}/history", protect(false, s.workerVersionHistory))
	m.HandleFunc("GET "+base+"/operations", protect(false, s.workerOperationList))
	m.HandleFunc("GET "+base+"/previews-base", protect(false, s.workerPreviewBase))
	m.HandleFunc("PATCH "+base+"/previews-base", protect(false, s.workerPreviewBase))
	m.HandleFunc("PATCH "+base+"/previews-base/secrets", protect(false, s.workerSecretBatch))
	m.HandleFunc("PATCH "+base+"/environments/{environment}/secrets", protect(false, s.workerSecretBatch))
	m.HandleFunc("POST "+base+"/environments/{environment}/versions", protect(false, s.workerVersionUpload))
	m.HandleFunc("GET "+base+"/versions", protect(false, s.workerVersionList))
	m.HandleFunc("GET "+base+"/deployments", protect(false, s.workerDeploymentList))
	m.HandleFunc("GET "+base+"/deployments/{deployment}", protect(false, s.workerDeploymentList))
	m.HandleFunc("GET "+base+"/versions/{version}", protect(false, s.workerVersionGet))
	m.HandleFunc("POST "+base+"/environments/{environment}/invoke", s.workerInvoke)
	m.HandleFunc("GET "+base+"/operations/{operation}", protect(false, s.workerOperationGet))
	m.HandleFunc("GET "+base+"/artifacts/{artifact}", protect(false, s.workerArtifactGet))
}

func (s *Server) workerTarget(st *State, r *http.Request, env, action string, p Principal) (Target, error) {
	return s.workerApplication(r).WorkerTarget(st, workerRef(r), env, action, p)
}

func (s *Server) workerService(st *State, t Target) WorkerService {
	return s.workerApplication(nil).WorkerService(st, t)
}

func (s *Server) workerBuildCreate(st *State, p Principal, r *http.Request) (any, int, error) {
	var in workerapp.WorkerBuildCreateInput
	if e := bodyWithLimit(r, &in, workerBuildRequestBodyMax); e != nil {
		return nil, 0, e
	}
	out, outcome, err := s.workerApplication(r).WorkerBuildCreate(r.Context(), st, p, workerRef(r), in)
	return out, workerHTTPStatus(outcome), err
}

// Clients upload a source bundle (src/ + assets/) with the returned URL, then
// reference the key in POST /builds. Signing is offline; the upload goes
// directly to object storage.
func (s *Server) workerSourceUploadCreate(st *State, p Principal, r *http.Request) (any, int, error) {
	out, outcome, err := s.workerApplication(r).WorkerSourceUploadCreate(r.Context(), st, p, workerRef(r))
	return out, workerHTTPStatus(outcome), err
}

func (s *Server) workerBuildList(st *State, p Principal, r *http.Request) (any, int, error) {
	out, outcome, err := s.workerApplication(r).WorkerBuildList(r.Context(), st, p, workerRef(r))
	return out, workerHTTPStatus(outcome), err
}

func (s *Server) workerBuildGet(st *State, p Principal, r *http.Request) (any, int, error) {
	out, outcome, err := s.workerApplication(r).WorkerBuildGet(r.Context(), st, p, workerRef(r))
	return out, workerHTTPStatus(outcome), err
}

func (s *Server) workerBuildLogs(st *State, p Principal, r *http.Request) (any, int, error) {
	out, outcome, err := s.workerApplication(r).WorkerBuildLogs(r.Context(), st, p, workerRef(r))
	return out, workerHTTPStatus(outcome), err
}

func (s *Server) workerBuildSource(st *State, p Principal, r *http.Request) (any, int, error) {
	out, outcome, err := s.workerApplication(r).WorkerBuildSource(r.Context(), st, p, workerRef(r))
	return out, workerHTTPStatus(outcome), err
}

func (s *Server) workerEnvironments(st *State, p Principal, r *http.Request) (any, int, error) {
	out, outcome, err := s.workerApplication(r).WorkerEnvironments(r.Context(), st, p, workerRef(r))
	return out, workerHTTPStatus(outcome), err
}

// workerOverview is the console polling read model. It deliberately contains
// only public Workers fields and applies the same per-environment permissions
// as the individual collection endpoints.
func (s *Server) workerOverview(st *State, p Principal, r *http.Request) (any, int, error) {
	out, outcome, err := s.workerApplication(r).WorkerOverview(r.Context(), st, p, workerRef(r))
	return out, workerHTTPStatus(outcome), err
}

// workerServiceDomains returns the service-level domain selection together with
// the catalog, so the console can render the routing page in one round trip.
func (s *Server) workerServiceDomains(st *State, p Principal, r *http.Request) (any, int, error) {
	out, outcome, err := s.workerApplication(r).WorkerServiceDomains(r.Context(), st, p, workerRef(r))
	return out, workerHTTPStatus(outcome), err
}

// workerServiceDomainsUpdate replaces the service-level domain selection. Every
// environment of the service is re-homed onto the new set.
func (s *Server) workerServiceDomainsUpdate(st *State, p Principal, r *http.Request) (any, int, error) {
	t, e := s.workerTarget(st, r, appconfig.WorkerBaseline, "service.update", p)
	if e != nil {
		return nil, 0, e
	}
	var in workerapp.WorkerServiceDomainsUpdateInput
	if e = body(r, &in); e != nil {
		return nil, 0, e
	}
	out, outcome, err := s.workerApplication(r).WorkerServiceDomainsUpdate(r.Context(), st, p, workerRef(r), t, in)
	return out, workerHTTPStatus(outcome), err
}

func (s *Server) workerServiceNetworkProfileUpdate(st *State, p Principal, r *http.Request) (any, int, error) {
	t, err := s.workerTarget(st, r, appconfig.WorkerBaseline, "service.update", p)
	if err != nil {
		return nil, 0, err
	}
	var in workerapp.WorkerServiceNetworkProfileUpdateInput
	if err = body(r, &in); err != nil {
		return nil, 0, err
	}
	out, outcome, err := s.workerApplication(r).WorkerServiceNetworkProfileUpdate(r.Context(), st, p, workerRef(r), t, in)
	return out, workerHTTPStatus(outcome), err
}

// Previews are created explicitly; the durable baseline always exists and cannot be created or deleted.
func (s *Server) workerEnvironmentCreate(st *State, p Principal, r *http.Request) (any, int, error) {
	var in workerapp.WorkerEnvironmentCreateInput
	if e := body(r, &in); e != nil {
		return nil, 0, e
	}
	out, outcome, err := s.workerApplication(r).WorkerEnvironmentCreate(r.Context(), st, p, workerRef(r), in)
	return out, workerHTTPStatus(outcome), err
}

func (s *Server) workerEnvironmentDelete(st *State, p Principal, r *http.Request) (any, int, error) {
	out, outcome, err := s.workerApplication(r).WorkerEnvironmentDelete(r.Context(), st, p, workerRef(r))
	return out, workerHTTPStatus(outcome), err
}

func (s *Server) workerEnvironmentUpdate(st *State, p Principal, r *http.Request) (any, int, error) {
	env := r.PathValue("environment")
	if !appconfig.WorkerEnvValid(env) {
		return nil, 0, notFound()
	}
	t, e := s.workerTarget(st, r, env, "service.update", p)
	if e != nil {
		return nil, 0, e
	}
	var in workerapp.WorkerEnvironmentUpdateInput
	if e = body(r, &in); e != nil {
		return nil, 0, e
	}
	out, outcome, err := s.workerApplication(r).WorkerEnvironmentUpdate(r.Context(), st, p, workerRef(r), t, env, in)
	return out, workerHTTPStatus(outcome), err
}

func (s *Server) workerPublish(st *State, p Principal, r *http.Request) (any, int, error) {
	return s.workerDeploymentCreate(st, p, r, "publish")
}

func (s *Server) workerRollback(st *State, p Principal, r *http.Request) (any, int, error) {
	return s.workerDeploymentCreate(st, p, r, "rollback")
}

func (s *Server) workerDeploymentCreate(st *State, p Principal, r *http.Request, kind string) (any, int, error) {
	env := r.PathValue("environment")
	if !appconfig.WorkerEnvValid(env) {
		return nil, 0, notFound()
	}
	t, e := s.workerTarget(st, r, env, "service."+kind, p)
	if e != nil {
		return nil, 0, e
	}
	var in workerapp.WorkerDeploymentCreateInput
	if e = body(r, &in); e != nil {
		return nil, 0, e
	}
	out, outcome, err := s.workerApplication(r).WorkerDeploymentCreate(r.Context(), st, p, workerRef(r), t, env, kind, in)
	return out, workerHTTPStatus(outcome), err
}

func (s *Server) workerOperationGet(st *State, p Principal, r *http.Request) (any, int, error) {
	out, outcome, err := s.workerApplication(r).WorkerOperationGet(r.Context(), st, p, workerRef(r))
	return out, workerHTTPStatus(outcome), err
}

func (s *Server) workerVersionHistory(st *State, p Principal, r *http.Request) (any, int, error) {
	out, outcome, err := s.workerApplication(r).WorkerVersionHistory(r.Context(), st, p, workerRef(r))
	return out, workerHTTPStatus(outcome), err
}

func (s *Server) workerArtifactGet(st *State, p Principal, r *http.Request) (any, int, error) {
	out, outcome, err := s.workerApplication(r).WorkerArtifactGet(r.Context(), st, p, workerRef(r))
	return out, workerHTTPStatus(outcome), err
}

func (s *Server) workerAuthorizeActor(st *State, a WorkerActor, service, env, action string) error {
	return s.workerApplication(nil).WorkerAuthorizeActor(st, a, service, env, action)
}

func (s *Server) workerOperationList(st *State, p Principal, r *http.Request) (any, int, error) {
	out, outcome, err := s.workerApplication(r).WorkerOperationList(r.Context(), st, p, workerRef(r))
	return out, workerHTTPStatus(outcome), err
}

func (s *Server) workerVersionGet(st *State, p Principal, r *http.Request) (any, int, error) {
	out, outcome, err := s.workerApplication(r).WorkerVersionGet(r.Context(), st, p, workerRef(r))
	return out, workerHTTPStatus(outcome), err
}
