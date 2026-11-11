package cfcompat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/krun-dev/krun/internal/authorization"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/storage"
	workerapp "github.com/krun-dev/krun/internal/workers"
)

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

func Result(value any) map[string]any {
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

func Response(value any) any {
	if pending, ok := value.(Deferred); ok {
		return deferred{run: func(ctx context.Context) (any, int, error) {
			out, _, err := pending.Run(ctx)
			return Result(out), 200, err
		}}
	}
	return Result(value)
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
			s.JSON(w, b.status, map[string]any{"success": false, "errors": []any{map[string]any{"code": code, "message": payload.Error.Message}}, "messages": []any{}, "result": nil})
			return
		}
		w.WriteHeader(b.status)
		_, _ = w.Write(b.body.Bytes())
	}
}

func (s *Server) cfProtected(fn Endpoint) http.HandlerFunc {
	return s.cfTransport(func(w http.ResponseWriter, r *http.Request) {
		var err error
		r, err = s.cfRequest(r)
		if err != nil {
			s.WriteError(w, r, err)
			return
		}
		s.ProtectedWorkers(false, workerapp.WorkerBuildRequestBodyMax, func(st *domain.State, p authorization.Principal, r *http.Request) (any, int, error) {
			out, _, err := fn(st, p, r)
			return Response(out), 200, err
		})(w, r)
	})
}

func (s *Server) cfRequest(r *http.Request) (*http.Request, error) {
	if s.Config.Workers == nil {
		return r, domain.Problem(503, "WORKERS_NOT_CONFIGURED", "Workers is not configured")
	}
	c, err := s.ReadCredentials(r)
	if err != nil {
		return r, err
	}
	if !c.HasSession && c.KeyID == "" {
		return r, domain.Problem(401, "UNAUTHENTICATED", "Authentication required")
	}
	if err = s.ResolveAccount(r); err != nil {
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
			return r, domain.NotFound()
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
	return s.ScopedRequest(r, c)
}

func (s *Server) cfCall(r *http.Request, write bool, fn func(*domain.State, authorization.Principal) error) error {
	c, err := s.ReadCredentials(r)
	if err != nil {
		return err
	}
	if !c.HasSession && c.KeyID == "" {
		return domain.Problem(401, "UNAUTHENTICATED", "Authentication required")
	}
	r, err = s.ScopedRequest(r, c)
	if err != nil {
		return err
	}
	persist := storage.StatePartition(0)
	if write {
		persist = storage.PartitionWorkers
	}
	return s.Store.Mutate(r.Context(), storage.PartitionAll, persist, func(st *domain.State) error {
		p, err := s.ProtectedPrincipal(st, c, r, false)
		if err != nil {
			return err
		}
		return fn(st, p)
	})
}

func (s *Server) cfEnsureScript(r *http.Request, name string) (string, error) {
	if s.Config.Workers == nil {
		return "", domain.Problem(503, "WORKERS_NOT_CONFIGURED", "Workers is not configured")
	}
	if !cfScriptName.MatchString(name) {
		return "", domain.Problem(422, "INVALID_REQUEST", "Invalid Worker name")
	}
	c, err := s.ReadCredentials(r)
	if err != nil {
		return "", err
	}
	if !c.HasSession && c.KeyID == "" {
		return "", domain.Problem(401, "UNAUTHENTICATED", "Authentication required")
	}
	if err = s.ResolveAccount(r); err != nil {
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
	err = s.cfCall(r, true, func(st *domain.State, p authorization.Principal) error {
		target, err := s.WorkerTarget(st, r, "production", "worker.publish", p)
		if err != nil {
			return err
		}
		s.Application(r).Worker(st, target)
		return nil
	})
	return id, err
}

func (s *Server) cfCreateScriptResource(r *http.Request, c Credentials, name string) (string, error) {
	r, err := s.ScopedRequest(r, c)
	if err != nil {
		return "", err
	}
	var resource domain.Resource
	err = s.Store.Mutate(r.Context(), storage.PartitionIAM, storage.PartitionIAM, func(st *domain.State) error {
		p, err := s.ProtectedPrincipal(st, c, r, false)
		if err != nil {
			return err
		}
		target, err := s.OrgTarget(st, r)
		if err != nil {
			return err
		}
		if err = s.Require(st, p, r, "worker.create", target); err != nil {
			return err
		}
		resource, err = s.IAM().CreateWorkerScript(st, target, name)
		return err
	})
	return resource.ID, err
}

func isProblem(err error, code string) bool {
	var problem *domain.APIError
	return errors.As(err, &problem) && problem.Code == code
}

// Routes returns the Cloudflare-compatible endpoint catalog. objects and
// hyperdrive are native krun endpoints wrapped in the Cloudflare envelope.
func (s *Server) Routes(objects Endpoint) []Route {
	base := Prefix + "/accounts/{account}"
	routes := []Route{}
	add := func(pattern string, handler http.HandlerFunc, accountScope bool) {
		routes = append(routes, Route{Pattern: pattern, Handler: handler, AccountScope: accountScope})
	}
	configs := base + "/hyperdrive/configs"
	for _, method := range []string{"GET", "POST"} {
		add(method+" "+configs, s.cfProtected(s.cfHyperdrive), false)
	}
	for _, method := range []string{"GET", "PATCH", "DELETE"} {
		add(method+" "+configs+"/{hyperdrive}", s.cfProtected(s.cfHyperdrive), false)
	}
	add("GET "+base+"/r2/buckets", s.cfProtected(s.cfBucketList), false)
	add("POST "+base+"/r2/buckets", s.cfProtected(s.cfBucketCreate), false)
	add("GET "+base+"/r2/buckets/{bucket}", s.cfProtected(s.cfBucketList), false)
	add("GET "+base+"/r2/buckets/{bucket}/objects", s.cfProtected(objects), false)
	add("GET "+Prefix+"/memberships", s.cfProtected(s.cfMemberships), true)
	add("GET "+Prefix+"/user", s.cfProtected(s.cfUser), true)
	add("GET "+Prefix+"/accounts", s.cfProtected(s.cfAccounts), true)
	add("GET "+Prefix+"/user/tokens/verify", s.cfProtected(s.cfVerifyToken), true)
	add("GET "+base+"/workers/subdomain", s.cfProtected(s.cfAccountSubdomain), false)
	scripts := base + "/workers/scripts"
	add("GET "+scripts, s.cfProtected(s.cfScripts), false)
	add("POST "+scripts+"/{script}/assets-upload-session", s.cfTransport(s.cfAssetSession), false)
	add("POST "+base+"/workers/assets/upload", s.cfTransport(s.cfAssetUpload), false)
	add("POST "+base+"/workers/assets/upload/{hash}", s.cfTransport(s.cfAssetUpload), false)
	add("PUT "+scripts+"/{script}", s.cfTransport(s.cfUpload), false)
	add("DELETE "+scripts+"/{script}", s.cfTransport(s.cfDeleteScript), false)
	for _, path := range []string{scripts + "/{script}/settings", scripts + "/{script}/script-settings", base + "/workers/workers/{script}", base + "/workers/services/{script}", scripts + "/{script}/subdomain"} {
		add("GET "+path, s.cfProtected(s.cfScriptDetails), false)
	}
	add("POST "+scripts+"/{script}/subdomain", s.cfProtected(s.cfSubdomainUpdate), false)
	add("POST "+scripts+"/{script}/versions", s.cfTransport(s.cfUpload), false)
	add("GET "+scripts+"/{script}/versions", s.cfProtected(s.cfVersions), false)
	add("GET "+scripts+"/{script}/versions/{version}", s.cfProtected(s.cfVersions), false)
	add("GET "+scripts+"/{script}/deployments", s.cfProtected(s.cfDeployments), false)
	add("GET "+scripts+"/{script}/deployments/{deployment}", s.cfProtected(s.cfDeployments), false)
	add("POST "+scripts+"/{script}/deployments", s.cfTransport(s.cfCreateDeployment), false)
	add("GET "+scripts+"/{script}/secrets", s.cfProtected(s.cfSecrets), false)
	add("PUT "+scripts+"/{script}/secrets", s.cfTransport(s.cfSecretChange), false)
	add("DELETE "+scripts+"/{script}/secrets/{secret}", s.cfTransport(s.cfSecretChange), false)
	add("PATCH "+scripts+"/{script}/secrets-bulk", s.cfTransport(s.cfSecretChange), false)
	workers := base + "/workers/workers"
	add("POST "+workers, s.cfTransport(s.cfCreateWorker), false)
	add("GET "+workers+"/{script}/previews", s.cfProtected(s.cfPreviews), false)
	add("POST "+workers+"/{script}/previews", s.cfProtected(s.cfPreviews), false)
	add("GET "+workers+"/{script}/previews/{preview}", s.cfProtected(s.cfPreviews), false)
	add("DELETE "+workers+"/{script}/previews/{preview}", s.cfProtected(s.cfPreviews), false)
	add("POST "+workers+"/{script}/previews/{preview}/deployments", s.cfTransport(s.cfUpload), false)
	add("GET "+workers+"/{script}/previews/{preview}/deployments/{deployment}", s.cfProtected(s.cfPreviewDeployments), false)
	add("PATCH "+workers+"/{script}", s.cfProtected(s.cfPreviewBase), false)
	add("PATCH "+workers+"/{script}/previews/{preview}/deployments/{deployment}", s.cfTransport(s.cfSecretChange), false)
	add(Prefix+"/", s.cfTransport(func(w http.ResponseWriter, r *http.Request) { s.WriteError(w, r, domain.NotFound()) }), true)
	return routes
}

// Cloudflare uses merge-patch JSON for Secret and Preview configuration updates.
func (s *Server) body(r *http.Request, value any) error {
	copy := r.Clone(r.Context())
	copy.Header = r.Header.Clone()
	if strings.HasPrefix(copy.Header.Get("Content-Type"), "application/merge-patch+json") {
		copy.Header.Set("Content-Type", "application/json")
	}
	return s.DecodeBody(copy, value)
}
