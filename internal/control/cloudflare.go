package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/krun-dev/krun/internal/domain"
	workerapp "github.com/krun-dev/krun/internal/workers"
)

const cfPrefix = "/api/v1/cloudflare/client/v4"

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
	if page, ok := value.(domain.BucketObjectPage); ok {
		objects := make([]any, 0, len(page.Objects))
		for _, item := range page.Objects {
			objects = append(objects, map[string]any{"key": item.Key, "size": item.Size, "etag": item.ETag, "last_modified": item.Uploaded})
		}
		return map[string]any{
			"success": true, "errors": []any{}, "messages": []any{}, "result": objects,
			"result_info": map[string]any{"cursor": page.Cursor, "delimited": page.Prefixes, "is_truncated": page.Truncated, "per_page": page.PerPage},
		}
	}
	return map[string]any{"success": true, "errors": []any{}, "messages": []any{}, "result": value}
}

func cfResponse(value any) any {
	if deferred, ok := value.(deferredResponse); ok {
		return deferredResponse{run: func(ctx context.Context) (any, int, error) {
			out, _, err := deferred.run(ctx)
			return cfResult(out), 200, err
		}}
	}
	return cfResult(value)
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
			if payload.Error.Code == "STORAGE_BUCKET_NOT_FOUND" {
				code = 10006
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
			return cfResponse(out), 200, err
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
	if err = s.resolveCloudflareAccount(r); err != nil {
		return r, err
	}
	org := r.PathValue("org")
	name := r.PathValue("script")
	if name != "" {
		id, err := s.Store.ResourceID(r.Context(), org, "worker", name, false)
		if err != nil {
			return r, err
		}
		if expected := r.PathValue("resource"); expected != "" && expected != id {
			return r, notFound()
		}
		r.SetPathValue("resource", id)
	}
	if name == "" && strings.HasSuffix(r.URL.Path, "/workers/scripts") {
		scripts, err := s.Store.WorkerScripts(r.Context(), org)
		if err != nil {
			return r, err
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
func (s *Server) cfEnsureScript(r *http.Request, name string) (string, error) {
	if s.Config.Workers == nil {
		return "", problem(503, "WORKERS_NOT_CONFIGURED", "Workers is not configured")
	}
	if !cfScriptName.MatchString(name) {
		return "", problem(422, "INVALID_REQUEST", "Invalid Worker name")
	}
	c, err := s.readCredentials(r)
	if err != nil {
		return "", err
	}
	if !c.hasSession && c.keyID == "" {
		return "", problem(401, "UNAUTHENTICATED", "Authentication required")
	}
	if err = s.resolveCloudflareAccount(r); err != nil {
		return "", err
	}
	org := r.PathValue("org")
	id, err := s.Store.ResourceID(r.Context(), org, "worker", name, false)
	if isProblem(err, "RESOURCE_NOT_FOUND") {
		id, err = s.cfCreateScriptResource(r, c, name)
		if isProblem(err, "LOCATOR_CONFLICT") {
			// Both API surfaces race on the same unique locator. Load the
			// winning identity and authorize publishing to it below.
			id, err = s.Store.ResourceID(r.Context(), org, "worker", name, false)
		}
	}
	if err != nil {
		return "", err
	}
	r.SetPathValue("resource", id)
	err = s.cfCall(r, true, func(st *State, p Principal) error {
		target, err := s.workerTarget(st, r, "production", "worker.publish", p)
		if err != nil {
			return err
		}
		s.workerApplication(r).Worker(st, target)
		return nil
	})
	return id, err
}

func (s *Server) cfCreateScriptResource(r *http.Request, c authCredentials, name string) (string, error) {
	r, err := s.scopedRequest(r, c)
	if err != nil {
		return "", err
	}
	var resource domain.Resource
	err = s.Store.Mutate(r.Context(), PartitionIAM, PartitionIAM, func(st *State) error {
		p, err := s.protectedPrincipal(st, c, r, false)
		if err != nil {
			return err
		}
		target, err := s.orgTarget(st, r)
		if err != nil {
			return err
		}
		if err = s.require(st, p, r, "worker.create", target); err != nil {
			return err
		}
		resource, err = s.iam().CreateWorkerScript(st, target, name)
		return err
	})
	return resource.ID, err
}

func isProblem(err error, code string) bool {
	var problem *domain.APIError
	return errors.As(err, &problem) && problem.Code == code
}

func (s *Server) cloudflareRoutes(m *recordingMux) {
	base := cfPrefix + "/accounts/{account}"
	hyperdrive := base + "/hyperdrive/configs"
	m.useScope(scopeCloudflareOrganization)
	for _, method := range []string{"GET", "POST"} {
		m.HandleFunc(method+" "+hyperdrive, s.cfProtected(s.cfHyperdrive))
	}
	for _, method := range []string{"GET", "PATCH", "DELETE"} {
		m.HandleFunc(method+" "+hyperdrive+"/{hyperdrive}", s.cfProtected(s.cfHyperdrive))
	}
	m.HandleFunc("GET "+base+"/r2/buckets", s.cfProtected(s.cfBucketList))
	m.HandleFunc("POST "+base+"/r2/buckets", s.cfProtected(s.cfBucketCreate))
	m.HandleFunc("GET "+base+"/r2/buckets/{bucket}", s.cfProtected(s.cfBucketList))
	m.HandleFunc("GET "+base+"/r2/buckets/{bucket}/objects", s.cfProtected(s.workerObjects))
	m.useScope(scopeCloudflareAccounts)
	m.HandleFunc("GET "+cfPrefix+"/memberships", s.cfProtected(s.cfMemberships))
	m.HandleFunc("GET "+cfPrefix+"/user", s.cfProtected(s.cfUser))
	m.HandleFunc("GET "+cfPrefix+"/accounts", s.cfProtected(s.cfAccounts))
	m.HandleFunc("GET "+cfPrefix+"/user/tokens/verify", s.cfProtected(s.cfVerifyToken))
	m.useScope(scopeCloudflareOrganization)
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
	m.useScope(scopeCloudflareAccounts)
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
