package control

import (
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	workerartifact "github.com/krun-dev/krun/internal/artifact"
	"github.com/krun-dev/krun/internal/securetoken"
	workerapp "github.com/krun-dev/krun/internal/workers"
)

type cfBinding struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Text string `json:"text"`
}
type cfUploadMetadata struct {
	Main     string                `json:"main_module"`
	Date     string                `json:"compatibility_date"`
	Flags    []string              `json:"compatibility_flags"`
	Bindings []cfBinding           `json:"bindings"`
	Env      map[string]*cfBinding `json:"env"`
	Keep     []string              `json:"keep_bindings"`
	Assets   json.RawMessage       `json:"assets"`
}

func cfReadUpload(r *http.Request) (cfUploadMetadata, []workerartifact.UploadedModule, error) {
	var meta cfUploadMetadata
	r.Body = http.MaxBytesReader(nil, r.Body, workerapp.WorkerBuildRequestBodyMax)
	reader, err := r.MultipartReader()
	if err != nil {
		return meta, nil, problem(400, "INVALID_REQUEST", "Expected multipart/form-data")
	}
	modules := []workerartifact.UploadedModule{}
	seen := false
	for {
		part, e := reader.NextPart()
		if e == io.EOF {
			break
		}
		if e != nil {
			return meta, nil, problem(400, "INVALID_REQUEST", "Invalid multipart body")
		}
		limit := int64(workerartifact.WorkerModuleMax)
		if part.FormName() == "metadata" {
			limit = 1 << 20
		}
		data, e := io.ReadAll(io.LimitReader(part, limit+1))
		if e != nil || int64(len(data)) > limit {
			return meta, nil, problem(413, "INVALID_REQUEST", "Upload part too large")
		}
		if part.FormName() == "metadata" {
			if seen || json.Unmarshal(data, &meta) != nil {
				return meta, nil, problem(400, "INVALID_REQUEST", "Invalid upload metadata")
			}
			seen = true
			continue
		}
		if len(modules) >= workerartifact.WorkerModulesMax {
			return meta, nil, problem(413, "INVALID_REQUEST", "Too many modules")
		}
		_, disposition, _ := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
		name := disposition["filename"]
		if name == "" {
			name = part.FormName()
		}
		// FileName strips directories; the form name preserves module import paths.
		if part.FormName() != "files" && part.FormName() != "" {
			name = part.FormName()
		}
		kind, _, _ := mime.ParseMediaType(part.Header.Get("Content-Type"))
		typ := ""
		switch kind {
		case "application/javascript+module", "text/javascript+module":
			typ = "esm"
		case "application/javascript", "text/javascript":
			typ = "commonjs"
		case "text/plain":
			typ = "text"
		case "application/octet-stream":
			typ = "data"
		case "application/wasm":
			typ = "wasm"
		case "application/source-map":
			typ = "source-map"
		}
		if typ == "" {
			return meta, nil, problem(422, "UNSUPPORTED_MODULE", "Unsupported module content type")
		}
		modules = append(modules, workerartifact.UploadedModule{Name: name, Type: typ, Data: data})
	}
	if !seen {
		return meta, nil, problem(400, "INVALID_REQUEST", "Missing metadata")
	}
	if len(meta.Assets) > 0 && string(meta.Assets) != "null" {
		return meta, nil, problem(422, "UNSUPPORTED_ASSETS", "Assets upload sessions are not supported by this API")
	}
	return meta, modules, nil
}
func cfBindingInput(meta cfUploadMetadata, preview bool) (workerapp.BindingsInput, error) {
	in := workerapp.BindingsInput{Variables: map[string]string{}, Secrets: map[string]*string{}, ReplaceVariables: !preview, KeepSecrets: preview}
	for _, kind := range meta.Keep {
		if kind == "secret_text" {
			in.KeepSecrets = true
		} else if kind != "secret_key" {
			return in, problem(422, "UNSUPPORTED_BINDING", "Unsupported keep_bindings type")
		}
	}
	seen := map[string]bool{}
	for _, b := range meta.Bindings {
		if seen[b.Name] {
			return in, problem(422, "INVALID_REQUEST", "Duplicate binding name")
		}
		seen[b.Name] = true
		switch b.Type {
		case "plain_text":
			in.Variables[b.Name] = b.Text
		case "secret_text":
			value := b.Text
			in.Secrets[b.Name] = &value
		case "inherit":
			in.KeepSecrets = true
			in.Inherit = append(in.Inherit, b.Name)
		default:
			return in, problem(422, "UNSUPPORTED_BINDING", "Unsupported binding type: "+b.Type)
		}
	}
	for name, b := range meta.Env {
		if b == nil {
			in.Secrets[name] = nil
			continue
		}
		switch b.Type {
		case "plain_text":
			in.Variables[name] = b.Text
		case "secret_text":
			value := b.Text
			in.Secrets[name] = &value
		case "inherit":
			in.KeepSecrets = true
			in.Inherit = append(in.Inherit, name)
		default:
			return in, problem(422, "UNSUPPORTED_BINDING", "Unsupported Preview binding type")
		}
	}
	return in, nil
}
func (s *Server) cfUpload(w http.ResponseWriter, r *http.Request) {
	// Authenticate before consuming a body or creating an IAM resource.
	c, err := s.readCredentials(r)
	if err != nil || !c.hasSession && c.keyID == "" {
		if err == nil {
			err = problem(401, "UNAUTHENTICATED", "Authentication required")
		}
		s.writeError(w, r, err)
		return
	}
	meta, modules, err := cfReadUpload(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	input, err := cfBindingInput(meta, r.PathValue("preview") != "")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	raw, err := workerartifact.PackModules(meta.Main, meta.Date, meta.Flags, modules, nil, nil)
	if err != nil {
		s.writeError(w, r, problem(422, "WORKER_ARCHIVE_INVALID", err.Error()))
		return
	}
	script, err := s.cfEnsureScript(r, r.PathValue("script"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	r, err = s.cfRequest(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var actor WorkerActor
	err = s.cfCall(r, false, func(st *State, p Principal) error {
		_, e := s.workerTarget(st, r, r.PathValue("environment"), "service.publish", p)
		actor = workerapp.WorkerActor(p)
		return e
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if s.workers == nil {
		s.writeError(w, r, problem(503, "WORKERS_NOT_CONFIGURED", "Worker upload controller unavailable"))
		return
	}
	build, err := s.workers.ImportModules(r.Context(), workerRef(r), actor, raw)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var version WorkerVersion
	var op WorkerOperation
	deploy := !strings.HasSuffix(r.URL.Path, "/versions")
	err = s.cfCall(r, true, func(st *State, p Principal) error {
		app := s.workerApplication(r)
		ref := workerRef(r)
		if e := app.StageBindings(st, p, ref, input); e != nil {
			return e
		}
		out, _, e := app.WorkerVersionUpload(r.Context(), st, p, ref, workerapp.WorkerVersionUploadInput{Build: build.ID, Key: securetoken.UUID()})
		if e != nil {
			return e
		}
		version = out.(WorkerVersion)
		if !deploy {
			return nil
		}
		target, e := s.workerTarget(st, r, ref.Environment, "service.publish", p)
		if e != nil {
			return e
		}
		env := st.Workers.Services[script.Service].Environments[ref.Environment]
		out, _, e = app.WorkerDeploymentCreate(r.Context(), st, p, ref, target, ref.Environment, "publish", workerapp.WorkerDeploymentCreateInput{Version: version.ID, Revision: env.Revision, Key: securetoken.UUID()})
		if e == nil {
			op = out.(WorkerOperation)
		}
		return e
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if deploy {
		if err = s.cfWait(r, op.ID); err != nil {
			s.writeError(w, r, err)
			return
		}
	}
	result := cfVersion(version, 1)
	result["version_id"] = version.ID
	result["id"] = version.ID
	result["etag"] = version.ArchiveDigest
	if op.ID != "" {
		err = s.cfCall(r, false, func(st *State, p Principal) error {
			for _, d := range st.Workers.Deployments {
				if d.Operation != op.ID {
					continue
				}
				result["urls"] = s.cfDeploymentURLs(st, d)
				if r.PathValue("preview") != "" {
					result["id"] = d.ID
				} else {
					// The legacy Script upload field is named deployment_id, but Cloudflare
					// and Wrangler use it as the uploaded Version UUID.
					result["id"] = script.Name
					result["deployment_id"] = version.ID
				}
				env := st.Workers.Services[script.Service].Environments[r.PathValue("environment")]
				result["env"] = cfEnvBindings(env)
				return nil
			}
			return notFound()
		})
		if err != nil {
			s.writeError(w, r, err)
			return
		}
	}

	s.json(w, 200, cfResult(result))
}
func (s *Server) cfWait(r *http.Request, id string) error {
	timer := time.NewTimer(150 * time.Second)
	defer timer.Stop()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		var op WorkerOperation
		err := s.cfCall(r, false, func(st *State, p Principal) error {
			var ok bool
			op, ok = st.Workers.Operations[id]
			if !ok {
				return notFound()
			}
			return nil
		})
		if err != nil {
			return err
		}
		switch op.State {
		case "available", "committed", "succeeded":
			return nil
		case "failed", "cancelled":
			return problem(422, "DEPLOYMENT_FAILED", op.Error)
		}
		select {
		case <-r.Context().Done():
			return r.Context().Err()
		case <-timer.C:
			return problem(504, "DEPLOYMENT_PENDING", "Deployment is still processing; inspect deployments before retrying")
		case <-tick.C:
		}
	}
}
