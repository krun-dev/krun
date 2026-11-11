package cfcompat

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	workerartifact "github.com/krun-dev/krun/internal/artifact"
	"github.com/krun-dev/krun/internal/authorization"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/securetoken"
	workerapp "github.com/krun-dev/krun/internal/workers"
)

type Binding struct {
	ID               string          `json:"id,omitempty"`
	Password         *string         `json:"password,omitempty"`
	ConnectionString *string         `json:"connection_string,omitempty"`
	BucketName       string          `json:"bucket_name,omitempty"`
	Jurisdiction     string          `json:"jurisdiction,omitempty"`
	Name             string          `json:"name"`
	Type             string          `json:"type"`
	Text             string          `json:"text"`
	JSON             json.RawMessage `json:"json"`
}
type UploadMetadata struct {
	Main        string              `json:"main_module"`
	Date        string              `json:"compatibility_date"`
	Flags       []string            `json:"compatibility_flags"`
	Bindings    []Binding           `json:"bindings"`
	Env         map[string]*Binding `json:"env"`
	Keep        []string            `json:"keep_bindings"`
	Assets      json.RawMessage     `json:"assets"`
	KeepAssets  bool                `json:"keep_assets"`
	Annotations map[string]string   `json:"annotations"`
}

func ReadUpload(r *http.Request) (UploadMetadata, []workerartifact.UploadedModule, error) {
	var meta UploadMetadata
	r.Body = http.MaxBytesReader(nil, r.Body, workerapp.WorkerBuildRequestBodyMax)
	reader, err := r.MultipartReader()
	if err != nil {
		return meta, nil, domain.Problem(400, "INVALID_REQUEST", "Expected multipart/form-data")
	}
	modules := []workerartifact.UploadedModule{}
	seen := false
	for {
		part, e := reader.NextPart()
		if e == io.EOF {
			break
		}
		if e != nil {
			return meta, nil, domain.Problem(400, "INVALID_REQUEST", "Invalid multipart body")
		}
		limit := int64(workerartifact.WorkerModuleMax)
		if part.FormName() == "metadata" {
			limit = 1 << 20
		}
		data, e := io.ReadAll(io.LimitReader(part, limit+1))
		if e != nil || int64(len(data)) > limit {
			return meta, nil, domain.Problem(413, "INVALID_REQUEST", "Upload part too large")
		}
		if part.FormName() == "metadata" {
			if seen || json.Unmarshal(data, &meta) != nil {
				return meta, nil, domain.Problem(400, "INVALID_REQUEST", "Invalid upload metadata")
			}
			seen = true
			continue
		}
		if len(modules) >= workerartifact.WorkerModulesMax {
			return meta, nil, domain.Problem(413, "INVALID_REQUEST", "Too many modules")
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
			return meta, nil, domain.Problem(422, "UNSUPPORTED_MODULE", "Unsupported module content type")
		}
		modules = append(modules, workerartifact.UploadedModule{Name: name, Type: typ, Data: data})
	}
	if !seen {
		return meta, nil, domain.Problem(400, "INVALID_REQUEST", "Missing metadata")
	}
	return meta, modules, nil
}
func BindingInput(meta UploadMetadata, preview bool) (workerapp.BindingsInput, error) {
	in := workerapp.BindingsInput{Variables: map[string]any{}, Secrets: map[string]*string{}, ReplaceVariables: !preview, KeepSecrets: preview}
	if !preview {
		values := map[string]string{}
		in.R2Buckets = &values
		hyperdrive := map[string]string{}
		in.HyperdriveBindings = &hyperdrive
		in.VersionMetadata = new(string)
		in.AssetsBinding = new(string)
	}
	for _, kind := range meta.Keep {
		if kind == "secret_text" {
			in.KeepSecrets = true
		} else if kind == "plain_text" {
			in.KeepPlainVariables = true
		} else if kind == "json" {
			in.KeepJSONVariables = true
		} else if kind == "r2_bucket" {
			in.KeepR2Buckets = true
		} else if kind == "hyperdrive" {
			in.KeepHyperdrive = true
		} else if kind != "secret_key" {
			return in, domain.Problem(422, "UNSUPPORTED_BINDING", "Unsupported keep_bindings type")
		}
	}
	seen := map[string]bool{}
	apply := func(name string, b *Binding) error {
		if seen[name] {
			return domain.Problem(422, "INVALID_REQUEST", "Duplicate binding name")
		}
		seen[name] = true
		if b == nil {
			in.Secrets[name] = nil
			return nil
		}
		switch b.Type {
		case "r2_bucket":
			if b.BucketName == "" || b.Jurisdiction != "" && b.Jurisdiction != "default" {
				return domain.Problem(422, "UNSUPPORTED_BINDING", "Invalid R2 bucket binding or unsupported jurisdiction")
			}
			if in.R2Buckets == nil {
				values := map[string]string{}
				in.R2Buckets = &values
			}
			(*in.R2Buckets)[name] = b.BucketName
		case "hyperdrive":
			if b.ID == "" || b.Password != nil || b.ConnectionString != nil {
				return domain.Problem(422, "UNSUPPORTED_BINDING", "Hyperdrive binding requires a configuration ID")
			}
			if in.HyperdriveBindings == nil {
				values := map[string]string{}
				in.HyperdriveBindings = &values
			}
			(*in.HyperdriveBindings)[name] = b.ID
		case "plain_text":
			in.Variables[name] = b.Text
		case "json":
			var value any
			decoder := json.NewDecoder(bytes.NewReader(b.JSON))
			decoder.UseNumber()
			if len(b.JSON) == 0 || decoder.Decode(&value) != nil {
				return domain.Problem(422, "INVALID_REQUEST", "Invalid JSON binding")
			}
			in.Variables[name] = value
		case "secret_text":
			value := b.Text
			in.Secrets[name] = &value
		case "version_metadata":
			if in.VersionMetadata != nil && *in.VersionMetadata != "" {
				return domain.Problem(422, "INVALID_REQUEST", "Only one version metadata binding is supported")
			}
			value := name
			in.VersionMetadata = &value
		case "assets":
			if in.AssetsBinding != nil && *in.AssetsBinding != "" {
				return domain.Problem(422, "INVALID_REQUEST", "Only one assets binding is supported")
			}
			value := name
			in.AssetsBinding = &value
		case "inherit":
			in.KeepSecrets = true
			in.Inherit = append(in.Inherit, name)
		default:
			return domain.Problem(422, "UNSUPPORTED_BINDING", "Unsupported binding type: "+b.Type)
		}
		return nil
	}
	for _, b := range meta.Bindings {
		if err := apply(b.Name, &b); err != nil {
			return in, err
		}
	}
	for name, b := range meta.Env {
		if err := apply(name, b); err != nil {
			return in, err
		}
	}
	return in, nil
}
func (s *Server) cfUpload(w http.ResponseWriter, r *http.Request) {
	// Authenticate before consuming a body or creating an IAM resource.
	c, err := s.ReadCredentials(r)
	if err != nil || !c.HasSession && c.KeyID == "" {
		if err == nil {
			err = domain.Problem(401, "UNAUTHENTICATED", "Authentication required")
		}
		s.WriteError(w, r, err)
		return
	}
	meta, modules, err := ReadUpload(r)
	if err != nil {
		s.WriteError(w, r, err)
		return
	}
	input, err := BindingInput(meta, r.PathValue("preview") != "")
	if err != nil {
		s.WriteError(w, r, err)
		return
	}
	workerID, err := s.cfEnsureScript(r, r.PathValue("script"))
	if err != nil {
		s.WriteError(w, r, err)
		return
	}
	r, err = s.cfRequest(r)
	if err != nil {
		s.WriteError(w, r, err)
		return
	}
	var actor domain.WorkerActor
	err = s.cfCall(r, false, func(st *domain.State, p authorization.Principal) error {
		_, e := s.WorkerTarget(st, r, r.PathValue("environment"), "worker.publish", p)
		actor = workerapp.WorkerActor(p)
		return e
	})
	if err != nil {
		s.WriteError(w, r, err)
		return
	}
	if s.Workers == nil {
		s.WriteError(w, r, domain.Problem(503, "WORKERS_NOT_CONFIGURED", "domain.Worker upload controller unavailable"))
		return
	}
	assets, files, err := s.cfUploadedAssets(r, meta, input)
	if err != nil {
		s.WriteError(w, r, err)
		return
	}
	if meta.Main == "" && assets != nil {
		meta.Main = "index.js"
		modules = []workerartifact.UploadedModule{{Name: meta.Main, Type: "esm", Data: []byte(`export default {fetch(){return new Response("Not Found",{status:404})}}`)}}
	}
	raw, err := workerartifact.PackModules(meta.Main, meta.Date, meta.Flags, modules, assets, files)
	if err != nil {
		s.WriteError(w, r, domain.Problem(422, "WORKER_ARCHIVE_INVALID", err.Error()))
		return
	}
	build, err := s.Workers.ImportModules(r.Context(), workerRef(r), actor, raw)
	if err != nil {
		s.WriteError(w, r, err)
		return
	}
	var version domain.WorkerVersion
	var op domain.WorkerOperation
	deploy := !strings.HasSuffix(r.URL.Path, "/versions")
	err = s.cfCall(r, true, func(st *domain.State, p authorization.Principal) error {
		app := s.Application(r)
		ref := workerRef(r)
		if e := app.StageBindings(r.Context(), st, p, ref, input); e != nil {
			return e
		}
		out, _, e := app.WorkerVersionUpload(r.Context(), st, p, ref, workerapp.WorkerVersionUploadInput{Build: build.ID, Key: securetoken.UUID(), Tag: meta.Annotations["workers/tag"], Message: meta.Annotations["workers/message"]})
		if e != nil {
			return e
		}
		version = out.(domain.WorkerVersion)
		if !deploy {
			return nil
		}
		target, e := s.WorkerTarget(st, r, ref.Environment, "worker.publish", p)
		if e != nil {
			return e
		}
		env := st.Workers.Workers[workerID].Environments[ref.Environment]
		out, _, e = app.WorkerDeploymentCreate(r.Context(), st, p, ref, target, ref.Environment, "publish", workerapp.WorkerDeploymentCreateInput{Version: version.ID, Revision: env.Revision, Key: securetoken.UUID()})
		if e == nil {
			op = out.(domain.WorkerOperation)
		}
		return e
	})
	if err != nil {
		s.WriteError(w, r, err)
		return
	}
	if deploy {
		if err = s.cfWait(r, op.ID); err != nil {
			s.WriteError(w, r, err)
			return
		}
	}
	result := cfVersion(version, 1)
	result["version_id"] = version.ID
	result["id"] = version.ID
	result["etag"] = version.ArchiveDigest
	if op.ID != "" {
		err = s.cfCall(r, false, func(st *domain.State, p authorization.Principal) error {
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
					result["id"] = r.PathValue("script")
					result["deployment_id"] = version.ID
				}
				env := st.Workers.Workers[workerID].Environments[r.PathValue("environment")]
				result["env"] = cfEnvBindings(env)
				return nil
			}
			return domain.NotFound()
		})
		if err != nil {
			s.WriteError(w, r, err)
			return
		}
	}

	s.JSON(w, 200, Result(result))
}
func (s *Server) cfWait(r *http.Request, id string) error {
	timer := time.NewTimer(150 * time.Second)
	defer timer.Stop()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		var op domain.WorkerOperation
		err := s.cfCall(r, false, func(st *domain.State, p authorization.Principal) error {
			var ok bool
			op, ok = st.Workers.Operations[id]
			if !ok {
				return domain.NotFound()
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
			return domain.Problem(422, "DEPLOYMENT_FAILED", op.Error)
		}
		select {
		case <-r.Context().Done():
			return r.Context().Err()
		case <-timer.C:
			return domain.Problem(504, "DEPLOYMENT_PENDING", "Deployment is still processing; inspect deployments before retrying")
		case <-tick.C:
		}
	}
}
