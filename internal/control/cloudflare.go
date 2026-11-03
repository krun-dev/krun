package control

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/securetoken"
	workerapp "github.com/krun-dev/krun/internal/workers"
)

const cfPrefix = "/api/v1/cloudflare/client/v4"

type cfScriptContext struct{}
type cfScriptsContext struct{}

var cfScriptName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// The adapter preserves the ordinary authentication, CSRF and request-scope
// machinery. Only the protocol envelope and numeric errors differ.
type cfBuffer struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func (b *cfBuffer) Header() http.Header    { return b.header }
func (b *cfBuffer) WriteHeader(status int) { b.status = status }
func (b *cfBuffer) Write(p []byte) (int, error) {
	if b.status == 0 {
		b.status = 200
	}
	return b.body.Write(p)
}
func cfResult(value any) map[string]any {
	return map[string]any{"success": true, "errors": []any{}, "messages": []any{}, "result": value}
}
func (s *Server) cfTransport(fn http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(180 * time.Second))
		}
		b := &cfBuffer{header: make(http.Header)}
		fn(b, r)
		if b.status == 0 {
			b.status = 200
		}
		for key, values := range b.header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		if b.status >= 400 {
			var payload struct {
				Error struct{ Code, Message string }
			}
			_ = json.Unmarshal(b.body.Bytes(), &payload)
			code := 10021
			if b.status == 401 || b.status == 403 {
				code = 10000
			}
			if b.status == 404 {
				code = 10007
			}
			if payload.Error.Code == "PREVIEW_NOT_FOUND" {
				code = 10025
			}
			if payload.Error.Code == "PREVIEW_DEPLOYMENT_NOT_FOUND" {
				code = 10222
			}
			if payload.Error.Code == "VERSION_NOT_DEPLOYED" {
				code = 10215
			}
			s.json(w, b.status, map[string]any{"success": false, "errors": []any{map[string]any{"code": code, "message": payload.Error.Message}}, "messages": []any{}, "result": nil})
			return
		}
		w.WriteHeader(b.status)
		_, _ = w.Write(b.body.Bytes())
	}
}
func (s *Server) cfProtected(fn endpoint) http.HandlerFunc {
	return s.cfTransport(func(w http.ResponseWriter, r *http.Request) {
		var err error
		r, err = s.cfRequest(r)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		s.protectedWorkersWithBodyLimit(false, workerapp.WorkerBuildRequestBodyMax, func(st *State, p Principal, r *http.Request) (any, int, error) {
			out, _, err := fn(st, p, r)
			return cfResult(out), 200, err
		})(w, r)
	})
}
func (s *Server) cfRequest(r *http.Request) (*http.Request, error) {
	if s.Config.Workers == nil {
		return r, problem(503, "WORKERS_NOT_CONFIGURED", "Workers is not configured")
	}
	c, err := s.readCredentials(r)
	if err != nil {
		return r, err
	}
	if !c.hasSession && c.keyID == "" {
		return r, problem(401, "UNAUTHENTICATED", "Authentication required")
	}
	r.SetPathValue("org", r.PathValue("account"))
	name := r.PathValue("script")
	if name != "" {
		script, ok, err := s.Store.WorkerScript(r.Context(), r.PathValue("account"), name)
		if err != nil {
			return r, err
		}
		if !ok {
			return r, notFound()
		}
		r.SetPathValue("resource", script.Service)
		r = r.WithContext(context.WithValue(r.Context(), cfScriptContext{}, script))
	}
	if name == "" && strings.HasSuffix(r.URL.Path, "/workers/scripts") {
		scripts, e := s.Store.WorkerScripts(r.Context(), r.PathValue("account"))
		if e != nil {
			return r, e
		}
		r = r.WithContext(context.WithValue(r.Context(), cfScriptsContext{}, scripts))
	}
	env := r.PathValue("preview")
	if env == "" {
		env = "production"
	}
	r.SetPathValue("environment", env)
	r, err = s.scopedRequest(r, c)
	return r, err
}
func (s *Server) cfCall(r *http.Request, write bool, fn func(*State, Principal) error) error {
	c, err := s.readCredentials(r)
	if err != nil {
		return err
	}
	if !c.hasSession && c.keyID == "" {
		return problem(401, "UNAUTHENTICATED", "Authentication required")
	}
	r, err = s.scopedRequest(r, c)
	if err != nil {
		return err
	}
	persist := StatePartition(0)
	if write {
		persist = PartitionWorkers
	}
	return s.Store.Mutate(r.Context(), PartitionAll, persist, func(st *State) error {
		p, err := s.protectedPrincipal(st, c, r, false)
		if err != nil {
			return err
		}
		return fn(st, p)
	})
}
func (s *Server) cfEnsureScript(r *http.Request, name string) (domain.WorkerScript, error) {
	if s.Config.Workers == nil {
		return domain.WorkerScript{}, problem(503, "WORKERS_NOT_CONFIGURED", "Workers is not configured")
	}
	if !cfScriptName.MatchString(name) {
		return domain.WorkerScript{}, problem(422, "INVALID_REQUEST", "Invalid Worker name")
	}
	org := r.PathValue("account")
	r.SetPathValue("org", org)
	c, err := s.readCredentials(r)
	if err != nil {
		return domain.WorkerScript{}, err
	}
	r, err = s.scopedRequest(r, c)
	if err != nil {
		return domain.WorkerScript{}, err
	}
	script, exists, err := s.Store.WorkerScript(r.Context(), org, name)
	if err != nil {
		return script, err
	}
	if !exists {
		err = s.Store.Mutate(r.Context(), PartitionIAM, 0, func(st *State) error {
			p, e := s.protectedPrincipal(st, c, r, false)
			if e != nil {
				return e
			}
			target, e := s.orgTarget(st, r)
			if e != nil {
				return e
			}
			return s.require(st, p, r, "service.create", target)
		})
		if err != nil {
			return script, err
		}
		candidate := domain.WorkerScript{Organization: org, Name: name, Service: securetoken.NewID("service"), WorkersDev: true, PreviewsEnabled: true}
		script, err = s.Store.ReserveWorkerScript(r.Context(), candidate)
		if err != nil {
			return script, err
		}
		exists = script.Service != candidate.Service
	}
	r.SetPathValue("resource", script.Service)
	r, err = s.scopedRequest(r, c)
	if err != nil {
		return script, err
	}
	err = s.Store.Mutate(r.Context(), PartitionIAM, PartitionIAM, func(st *State) error {
		p, e := s.protectedPrincipal(st, c, r, false)
		if e != nil {
			return e
		}
		if _, ok := st.Resources[script.Service]; ok {
			return nil
		}
		target, e := s.orgTarget(st, r)
		if e != nil {
			return e
		}
		if e = s.require(st, p, r, "service.create", target); e != nil {
			return e
		}
		// An existing alias without its resource may be an interrupted first
		// creation or a native service deletion. Never resurrect its identity
		// and thereby reconnect historical Versions or Secret references.
		if exists {
			return problem(409, "WORKER_IDENTITY_PENDING", "Worker identity is being created or cleaned up; retry after cleanup")
		}
		_, e = s.iam().EnsureWorkerResource(st, target, name, script.Service)
		return e
	})
	if err != nil {
		return script, err
	}
	r.SetPathValue("resource", script.Service)
	err = s.cfCall(r, true, func(st *State, p Principal) error {
		target, e := s.workerTarget(st, r, "production", "service.publish", p)
		if e != nil {
			return e
		}
		s.workerApplication(r).WorkerService(st, target)
		return nil
	})
	return script, err
}
func (s *Server) cloudflareRoutes(m *http.ServeMux) {
	base := cfPrefix + "/accounts/{account}"
	m.HandleFunc("GET "+cfPrefix+"/memberships", s.cfProtected(s.cfMemberships))
	m.HandleFunc("GET "+cfPrefix+"/user", s.cfProtected(s.cfUser))
	m.HandleFunc("GET "+cfPrefix+"/accounts", s.cfProtected(s.cfAccounts))
	m.HandleFunc("GET "+cfPrefix+"/user/tokens/verify", s.cfProtected(s.cfVerifyToken))
	m.HandleFunc("GET "+base+"/workers/subdomain", s.cfProtected(s.cfAccountSubdomain))
	scripts := base + "/workers/scripts"
	m.HandleFunc("GET "+scripts, s.cfProtected(s.cfScripts))
	m.HandleFunc("POST "+scripts+"/{script}/assets-upload-session", s.cfTransport(s.cfAssetSession))
	m.HandleFunc("POST "+base+"/workers/assets/upload", s.cfTransport(s.cfAssetUpload))
	m.HandleFunc("POST "+base+"/workers/assets/upload/{hash}", s.cfTransport(s.cfAssetUpload))
	m.HandleFunc("PUT "+scripts+"/{script}", s.cfTransport(s.cfUpload))
	m.HandleFunc("DELETE "+scripts+"/{script}", s.cfTransport(s.cfDeleteScript))
	for _, path := range []string{scripts + "/{script}/settings", scripts + "/{script}/script-settings", base + "/workers/workers/{script}", base + "/workers/services/{script}", scripts + "/{script}/subdomain"} {
		m.HandleFunc("GET "+path, s.cfProtected(s.cfScriptDetails))
	}
	m.HandleFunc("POST "+scripts+"/{script}/subdomain", s.cfProtected(s.cfSubdomainUpdate))
	m.HandleFunc("POST "+scripts+"/{script}/versions", s.cfTransport(s.cfUpload))
	m.HandleFunc("GET "+scripts+"/{script}/versions", s.cfProtected(s.cfVersions))
	m.HandleFunc("GET "+scripts+"/{script}/versions/{version}", s.cfProtected(s.cfVersions))
	m.HandleFunc("GET "+scripts+"/{script}/deployments", s.cfProtected(s.cfDeployments))
	m.HandleFunc("GET "+scripts+"/{script}/deployments/{deployment}", s.cfProtected(s.cfDeployments))
	m.HandleFunc("POST "+scripts+"/{script}/deployments", s.cfTransport(s.cfCreateDeployment))
	m.HandleFunc("GET "+scripts+"/{script}/secrets", s.cfProtected(s.cfSecrets))
	m.HandleFunc("PUT "+scripts+"/{script}/secrets", s.cfTransport(s.cfSecretChange))
	m.HandleFunc("DELETE "+scripts+"/{script}/secrets/{secret}", s.cfTransport(s.cfSecretChange))
	m.HandleFunc("PATCH "+scripts+"/{script}/secrets-bulk", s.cfTransport(s.cfSecretChange))
	workers := base + "/workers/workers"
	m.HandleFunc("POST "+workers, s.cfTransport(s.cfCreateWorker))
	m.HandleFunc("GET "+workers+"/{script}/previews", s.cfProtected(s.cfPreviews))
	m.HandleFunc("POST "+workers+"/{script}/previews", s.cfProtected(s.cfPreviews))
	m.HandleFunc("GET "+workers+"/{script}/previews/{preview}", s.cfProtected(s.cfPreviews))
	m.HandleFunc("DELETE "+workers+"/{script}/previews/{preview}", s.cfProtected(s.cfPreviews))
	m.HandleFunc("POST "+workers+"/{script}/previews/{preview}/deployments", s.cfTransport(s.cfUpload))
	m.HandleFunc("GET "+workers+"/{script}/previews/{preview}/deployments/{deployment}", s.cfProtected(s.cfPreviewDeployments))
	m.HandleFunc("PATCH "+workers+"/{script}", s.cfProtected(s.cfPreviewBase))
	m.HandleFunc("PATCH "+workers+"/{script}/previews/{preview}/deployments/{deployment}", s.cfTransport(s.cfSecretChange))
	m.HandleFunc(cfPrefix+"/", s.cfTransport(func(w http.ResponseWriter, r *http.Request) { s.writeError(w, r, notFound()) }))
}

// Cloudflare uses merge-patch JSON for Secret and Preview configuration updates.
func cfBody(r *http.Request, value any) error {
	copy := r.Clone(r.Context())
	copy.Header = r.Header.Clone()
	if strings.HasPrefix(copy.Header.Get("Content-Type"), "application/merge-patch+json") {
		copy.Header.Set("Content-Type", "application/json")
	}
	return body(copy, value)
}
