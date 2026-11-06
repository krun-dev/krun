package control

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/securetoken"
	workerapp "github.com/krun-dev/krun/internal/workers"
)

func (s *Server) cfCreateWorker(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name      string `json:"name"`
		Subdomain struct {
			Enabled         *bool `json:"enabled"`
			PreviewsEnabled *bool `json:"previews_enabled"`
		} `json:"subdomain"`
	}
	if err := cfBody(r, &in); err != nil {
		s.writeError(w, r, err)
		return
	}
	// Disabled managed URLs are explicitly unsupported until routing can enforce
	// this independently of the native environment's enabled state.
	if in.Subdomain.Enabled != nil && !*in.Subdomain.Enabled || in.Subdomain.PreviewsEnabled != nil && !*in.Subdomain.PreviewsEnabled {
		s.writeError(w, r, problem(422, "UNSUPPORTED_SETTING", "Disabling managed URLs is not supported"))
		return
	}
	_, err := s.cfEnsureScript(r, in.Name)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	r.SetPathValue("script", in.Name)
	r, err = s.cfRequest(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var out any
	err = s.cfCall(r, false, func(st *State, p Principal) error { var e error; out, _, e = s.cfScriptDetails(st, p, r); return e })
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.json(w, 200, cfResult(out))
}
func (s *Server) cfSubdomainUpdate(st *State, p Principal, r *http.Request) (any, int, error) {
	if _, err := s.workerTarget(st, r, "global", "worker.update", p); err != nil {
		return nil, 0, err
	}
	var in struct {
		Enabled         *bool `json:"enabled"`
		PreviewsEnabled *bool `json:"previews_enabled"`
	}
	if err := cfBody(r, &in); err != nil {
		return nil, 0, err
	}
	if in.Enabled != nil && !*in.Enabled || in.PreviewsEnabled != nil && !*in.PreviewsEnabled {
		return nil, 0, problem(422, "UNSUPPORTED_SETTING", "Disabling managed URLs is not supported")
	}
	_, env, err := s.cfCurrent(st, p, r)
	return map[string]any{"enabled": true, "previews_enabled": true, "url": s.cfURL(env)}, 200, err
}
func (s *Server) cfDeleteScript(w http.ResponseWriter, r *http.Request) {
	var err error
	r, err = s.cfRequest(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	c, err := s.readCredentials(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	r, err = s.scopedRequest(r, c)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	err = s.Store.Mutate(r.Context(), PartitionIAM, PartitionIAM, func(st *State) error {
		p, e := s.protectedPrincipal(st, c, r, false)
		if e != nil {
			return e
		}
		t, e := s.resourceTarget(st, r)
		if e != nil {
			return e
		}
		if e = s.require(st, p, r, "worker.delete", t); e != nil {
			return e
		}
		return s.iam().DeleteResource(st, t)
	})
	if err == nil {
		err = s.Store.DeleteWorkerScript(r.Context(), r.PathValue("org"), r.PathValue("script"))
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.json(w, 200, cfResult(nil))
}
func (s *Server) cfCreateDeployment(w http.ResponseWriter, r *http.Request) {
	var err error
	r, err = s.cfRequest(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var in struct {
		Strategy string `json:"strategy"`
		Versions []struct {
			ID         string  `json:"version_id"`
			Percentage float64 `json:"percentage"`
		} `json:"versions"`
		Annotations        map[string]string `json:"annotations"`
		CodeUpdateStrategy json.RawMessage   `json:"code_update_strategy"`
	}
	if err = cfBody(r, &in); err != nil {
		s.writeError(w, r, err)
		return
	}
	if len(in.Versions) != 1 || in.Versions[0].Percentage != 100 || in.Strategy != "" && in.Strategy != "percentage" {
		s.writeError(w, r, problem(422, "UNSUPPORTED_DEPLOYMENT", "Only one version at 100 percent is supported"))
		return
	}
	for key, value := range in.Annotations {
		if key != "workers/message" || value != "" {
			s.writeError(w, r, problem(422, "UNSUPPORTED_METADATA", "Deployment annotations are not supported"))
			return
		}
	}
	var op WorkerOperation
	err = s.cfCall(r, true, func(st *State, p Principal) error {
		ref := workerRef(r)
		t, e := s.workerTarget(st, r, ref.Environment, "worker.publish", p)
		if e != nil {
			return e
		}
		service := s.workerApplication(r).Worker(st, t)
		env := service.Environments[ref.Environment]
		out, _, e := s.workerApplication(r).WorkerDeploymentCreate(r.Context(), st, p, ref, t, ref.Environment, "publish", workerapp.WorkerDeploymentCreateInput{Version: in.Versions[0].ID, Revision: env.Revision, Key: securetoken.UUID()})
		if e == nil {
			op = out.(WorkerOperation)
		}
		return e
	})
	if err == nil {
		err = s.cfWait(r, op.ID)
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var out any
	err = s.cfCall(r, false, func(st *State, p Principal) error {
		for _, d := range st.Workers.Deployments {
			if d.Operation == op.ID {
				out = cfDeployment(d)
				return nil
			}
		}
		return notFound()
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.json(w, 200, cfResult(out))
}
func (s *Server) cfSecretChange(w http.ResponseWriter, r *http.Request) {
	var err error
	r, err = s.cfRequest(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if r.PathValue("preview") != "" && r.PathValue("deployment") != "latest" {
		s.writeError(w, r, problem(422, "UNSUPPORTED_DEPLOYMENT", "Only the latest Preview deployment can be patched"))
		return
	}
	secrets := map[string]*string{}
	if r.Method == http.MethodDelete {
		secrets[r.PathValue("secret")] = nil
	} else if strings.HasSuffix(r.URL.Path, "/secrets") {
		var in cfBinding
		if err = cfBody(r, &in); err == nil {
			if in.Type != "secret_text" {
				err = problem(422, "UNSUPPORTED_BINDING", "Expected secret_text")
			}
			secrets[in.Name] = &in.Text
		}
	} else {
		var in struct {
			Secrets     map[string]*cfBinding `json:"secrets"`
			Env         map[string]*cfBinding `json:"env"`
			Annotations map[string]string     `json:"annotations"`
		}
		err = cfBody(r, &in)
		bindings := in.Secrets
		if r.PathValue("preview") != "" {
			bindings = in.Env
		}
		for name, b := range bindings {
			if b == nil {
				secrets[name] = nil
			} else if b.Type != "secret_text" {
				err = problem(422, "UNSUPPORTED_BINDING", "Expected secret_text")
			} else {
				value := b.Text
				secrets[name] = &value
			}
		}
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var result any
	var op WorkerOperation
	err = s.cfCall(r, true, func(st *State, p Principal) error {
		ref := workerRef(r)
		target, e := s.workerTarget(st, r, ref.Environment, "worker.publish", p)
		if e != nil {
			return e
		}
		env := s.workerApplication(r).Worker(st, target).Environments[ref.Environment]
		if env.Version == "" {
			return problem(409, "VERSION_NOT_DEPLOYED", "Deploy a Worker before changing secrets")
		}
		// Ordinary Secret operations must not silently discard an uploaded draft.
		for _, v := range st.Workers.Versions {
			if v.Organization == ref.Organization && v.Worker == ref.Worker && v.Environment == ref.Environment && v.Created.After(st.Workers.Versions[env.Version].Created) {
				return problem(409, "VERSION_NOT_DEPLOYED", "Deploy the latest uploaded version before changing secrets")
			}
		}
		result, _, e = s.workerApplication(r).WorkerSecretBatch(r.Context(), st, p, ref, target, ref.Environment, workerapp.WorkerSecretBatchInput{Secrets: secrets, Revision: env.Revision, Key: securetoken.UUID()})
		if e == nil {
			op, _ = result.(WorkerOperation)
		}
		return e
	})
	if err == nil && op.ID != "" {
		err = s.cfWait(r, op.ID)
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out := any(nil)
	if r.PathValue("preview") != "" {
		r.SetPathValue("deployment", "latest")
		err = s.cfCall(r, false, func(st *State, p Principal) error {
			var e error
			out, _, e = s.cfPreviewDeployments(st, p, r)
			return e
		})
	} else if strings.HasSuffix(r.URL.Path, "/secrets-bulk") {
		out = map[string]any{"successful": len(secrets), "failed": 0}
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.json(w, 200, cfResult(out))
}
func (s *Server) cfPreviewBase(st *State, p Principal, r *http.Request) (any, int, error) {
	var in struct {
		Base struct {
			Env map[string]*cfBinding `json:"env"`
		} `json:"previews_base_config"`
	}
	if err := cfBody(r, &in); err != nil {
		return nil, 0, err
	}
	ref := workerRef(r)
	ref.Environment = domain.PreviewBaseName
	input, err := cfBindingInput(cfUploadMetadata{Env: in.Base.Env}, true)
	if err != nil {
		return nil, 0, err
	}
	if err = s.workerApplication(r).StageBindings(st, p, ref, input); err != nil {
		return nil, 0, err
	}
	return s.cfScriptDetails(st, p, r)
}
