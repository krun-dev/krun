package cfcompat

import (
	"maps"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/krun-dev/krun/internal/authorization"
	appconfig "github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/domain"
	workerapp "github.com/krun-dev/krun/internal/workers"
)

func (s *Server) cfAccounts(st *domain.State, p authorization.Principal, r *http.Request) (any, int, error) {
	if p.OAuthActions != nil && !slices.Contains(p.OAuthActions, "organization.read") {
		return nil, 0, domain.Forbidden()
	}
	out, _, err := s.OrgList(st, p, r)
	if err != nil {
		return nil, 0, err
	}
	if value, ok := out.(map[string]any); ok {
		return value["items"], 200, nil
	}
	return out, 200, nil
}
func (s *Server) cfVerifyToken(st *domain.State, p authorization.Principal, r *http.Request) (any, int, error) {
	id := p.User.ID
	if p.Key != nil {
		id = p.Key.ID
	}
	return map[string]any{"id": id, "status": "active"}, 200, nil
}
func (s *Server) cfAccountSubdomain(st *domain.State, p authorization.Principal, r *http.Request) (any, int, error) {
	t, err := s.OrgTarget(st, r)
	if err != nil {
		return nil, 0, err
	}
	if err = s.Require(st, p, r, "organization.read", t); err != nil {
		return nil, 0, err
	}
	return map[string]any{"subdomain": "krun"}, 200, nil
}
func (s *Server) cfScripts(st *domain.State, p authorization.Principal, r *http.Request) (any, int, error) {
	t, err := s.OrgTarget(st, r)
	if err != nil {
		return nil, 0, err
	}
	if err = s.Require(st, p, r, "worker.list", t); err != nil {
		return nil, 0, err
	}
	scripts, _ := r.Context().Value(cfScriptsContext{}).([]domain.Resource)
	out := []any{}
	for _, script := range scripts {
		ref := workerapp.WorkerRef{Organization: script.Organization, Worker: script.ID}
		if _, err := s.Application(r).WorkerCollectionTarget(st, ref, "worker.read", p); err != nil {
			continue
		}
		out = append(out, map[string]any{"id": script.Locator, "name": script.Locator, "tag": script.ID})
	}
	return out, 200, nil
}
func (s *Server) cfCurrent(st *domain.State, p authorization.Principal, r *http.Request) (domain.Worker, domain.WorkerEnvironment, error) {
	ref := workerRef(r)
	t, err := s.WorkerTarget(st, r, ref.Environment, "worker.read", p)
	if err != nil {
		return domain.Worker{}, domain.WorkerEnvironment{}, err
	}
	service := s.Application(r).WorkerView(st, t)
	env, ok := service.Environments[ref.Environment]
	if !ok {
		return service, env, domain.NotFound()
	}
	return service, env, nil
}
func Bindings(env domain.WorkerEnvironment) []any {
	out := []any{}
	names := make([]string, 0, len(env.R2Bindings))
	for name := range env.R2Bindings {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		out = append(out, map[string]any{"name": name, "type": "r2_bucket", "bucket_name": env.R2Bindings[name].BucketName})
	}
	for _, name := range slices.Sorted(maps.Keys(env.ServiceBindings)) {
		binding := env.ServiceBindings[name]
		out = append(out, map[string]any{"name": name, "type": "service", "service": binding.Service, "environment": binding.Environment})
	}
	for _, name := range slices.Sorted(maps.Keys(env.HyperdriveBindings)) {
		out = append(out, map[string]any{"name": name, "type": "hyperdrive", "id": env.HyperdriveBindings[name]})
	}
	keys := make([]string, 0, len(env.Variables))
	for name := range env.Variables {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	for _, name := range keys {
		if value, ok := env.Variables[name].(string); ok {
			out = append(out, map[string]any{"name": name, "type": "plain_text", "text": value})
		} else {
			out = append(out, map[string]any{"name": name, "type": "json", "json": env.Variables[name]})
		}
	}
	if env.VersionMetadata != "" {
		out = append(out, map[string]any{"name": env.VersionMetadata, "type": "version_metadata"})
	}
	if env.AssetsBinding != "" {
		out = append(out, map[string]any{"name": env.AssetsBinding, "type": "assets"})
	}
	for _, name := range domain.WorkerSecretNames(env.SecretVersions) {
		out = append(out, map[string]any{"name": name, "type": "secret_text"})
	}
	return out
}
func cfEnvBindings(env domain.WorkerEnvironment) map[string]any {
	out := map[string]any{}
	for _, raw := range Bindings(env) {
		binding := raw.(map[string]any)
		name := binding["name"].(string)
		copy := map[string]any{}
		for k, v := range binding {
			if k != "name" {
				copy[k] = v
			}
		}
		out[name] = copy
	}
	return out
}
func (s *Server) cfURL(env domain.WorkerEnvironment) string {
	if len(env.Hosts) == 0 {
		return ""
	}
	scheme := "https://"
	if s.Config.Development {
		scheme = "http://"
	}
	return scheme + env.Hosts[0]
}
func (s *Server) cfScriptDetails(st *domain.State, p authorization.Principal, r *http.Request) (any, int, error) {
	service, env, err := s.cfCurrent(st, p, r)
	if err != nil {
		return nil, 0, err
	}
	name := r.PathValue("script")
	workerID := r.PathValue("resource")
	version := st.Workers.Versions[env.Version]
	settings := map[string]any{"bindings": Bindings(env), "compatibility_date": version.CompatibilityDate, "compatibility_flags": version.CompatibilityFlags, "logpush": false, "tail_consumers": []any{}}
	subdomain := map[string]any{"enabled": true, "previews_enabled": true, "url": s.cfURL(env)}
	if strings.HasSuffix(r.URL.Path, "/settings") || strings.HasSuffix(r.URL.Path, "/script-settings") {
		return settings, 200, nil
	}
	if strings.HasSuffix(r.URL.Path, "/subdomain") {
		return subdomain, 200, nil
	}
	base := domain.WorkerEnvironment{}
	if service.PreviewBase != nil {
		base = *service.PreviewBase
	}
	return map[string]any{"id": name, "name": name, "tag": workerID, "subdomain": subdomain, "previews_base_config": map[string]any{"env": cfEnvBindings(base)}, "default_environment": map[string]any{"environment": "production", "script": map[string]any{"id": name, "tag": workerID, "last_deployed_from": "api", "compatibility_date": version.CompatibilityDate, "compatibility_flags": version.CompatibilityFlags, "bindings": Bindings(env)}}}, 200, nil
}
func cfVersion(v domain.WorkerVersion, number int) map[string]any {
	return map[string]any{"id": v.ID, "number": number, "metadata": map[string]any{"created_on": v.Created, "has_preview": false, "source": "api", "author_id": "krun", "author_email": ""}, "annotations": map[string]any{"workers/tag": v.Tag, "workers/message": v.Message}, "resources": map[string]any{"script": map[string]any{"etag": v.ArchiveDigest, "handlers": []string{"fetch"}}, "script_runtime": map[string]any{"compatibility_date": v.CompatibilityDate, "compatibility_flags": v.CompatibilityFlags}, "bindings": Bindings(domain.WorkerEnvironment{ServiceBindings: v.ServiceBindings, HyperdriveBindings: v.HyperdriveBindings, R2Bindings: v.R2Bindings, Variables: v.Variables, SecretVersions: v.SecretVersions, VersionMetadata: v.VersionMetadata, AssetsBinding: v.AssetsBinding})}}
}
func (s *Server) cfVersions(st *domain.State, p authorization.Principal, r *http.Request) (any, int, error) {
	out, _, err := s.Application(r).WorkerVersionList(r.Context(), st, p, workerRef(r))
	if err != nil {
		return nil, 0, err
	}
	versions := out.(map[string]any)["items"].([]domain.WorkerVersion)
	items := []any{}
	for i, v := range versions {
		if id := r.PathValue("version"); id != "" {
			if id == v.ID {
				return cfVersion(v, len(versions)-i), 200, nil
			}
			continue
		}
		items = append(items, cfVersion(v, len(versions)-i))
	}
	if r.PathValue("version") != "" {
		return nil, 0, domain.NotFound()
	}
	return map[string]any{"items": items}, 200, nil
}
func cfDeployment(d domain.WorkerDeployment) map[string]any {
	versions := []any{map[string]any{"version_id": d.Version, "percentage": float64(domain.WorkerFullWeight-d.CandidateWeight) / 1000}}
	if d.Candidate != "" {
		versions = append(versions, map[string]any{"version_id": d.Candidate, "percentage": float64(d.CandidateWeight) / 1000})
	}
	annotations := map[string]string{}
	if d.Message != "" {
		annotations["workers/message"] = d.Message
	}
	return map[string]any{"id": d.ID, "source": "api", "strategy": "percentage", "created_on": d.Created, "versions": versions, "annotations": annotations}
}
func (s *Server) cfDeployments(st *domain.State, p authorization.Principal, r *http.Request) (any, int, error) {
	ref := workerRef(r)
	ref.Environment = "production"
	if _, err := s.WorkerTarget(st, r, ref.Environment, "worker.version.read", p); err != nil {
		return nil, 0, err
	}
	items := []domain.WorkerDeployment{}
	for _, d := range st.Workers.Deployments {
		if d.Worker == ref.Worker && d.Organization == ref.Organization && d.Environment == ref.Environment && d.Committed != nil {
			items = append(items, d)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Created.After(items[j].Created) })
	out := []any{}
	for _, d := range items {
		if id := r.PathValue("deployment"); id != "" {
			if id == d.ID {
				return cfDeployment(d), 200, nil
			}
			continue
		}
		out = append(out, cfDeployment(d))
	}
	if r.PathValue("deployment") != "" {
		return nil, 0, domain.NotFound()
	}
	return map[string]any{"deployments": out}, 200, nil
}
func (s *Server) cfSecrets(st *domain.State, p authorization.Principal, r *http.Request) (any, int, error) {
	_, env, err := s.cfCurrent(st, p, r)
	if err != nil {
		return nil, 0, err
	}
	out := []any{}
	if env.VersionMetadata != "" {
		out = append(out, map[string]any{"name": env.VersionMetadata, "type": "version_metadata"})
	}
	if env.AssetsBinding != "" {
		out = append(out, map[string]any{"name": env.AssetsBinding, "type": "assets"})
	}
	for _, name := range domain.WorkerSecretNames(env.SecretVersions) {
		out = append(out, map[string]any{"name": name, "type": "secret_text"})
	}
	return out, 200, nil
}
func (s *Server) cfPreviewView(service domain.Worker, env domain.WorkerEnvironment) map[string]any {
	urls := []string{}
	if u := s.cfURL(env); u != "" {
		urls = append(urls, u)
	}
	return map[string]any{"name": env.Name, "id": env.Name, "env": cfEnvBindings(env), "urls": urls, "worker_id": service.Worker}
}
func (s *Server) cfPreviews(st *domain.State, p authorization.Principal, r *http.Request) (any, int, error) {
	app := s.Application(r)
	ref := workerRef(r)
	if r.Method == http.MethodPost {
		var in struct {
			Name string `json:"name"`
		}
		if err := s.body(r, &in); err != nil {
			return nil, 0, err
		}
		if !appconfig.WorkerPreviewValid(in.Name) {
			return nil, 0, domain.Problem(422, "ENVIRONMENT_INVALID", "Invalid Preview name")
		}
		ref.Environment = in.Name
		out, _, err := app.WorkerEnvironmentCreate(r.Context(), st, p, ref, workerapp.WorkerEnvironmentCreateInput{Name: in.Name, IgnoreBase: r.URL.Query().Get("ignore_base_config") == "true"})
		if err != nil {
			return nil, 0, err
		}
		env := out.(domain.WorkerEnvironment)
		service := st.Workers.Workers[ref.Worker]
		return s.cfPreviewView(service, env), 200, nil
	}
	if r.Method == http.MethodDelete {
		_, _, err := app.WorkerEnvironmentDelete(r.Context(), st, p, ref)
		return nil, 200, err
	}
	out, _, err := app.WorkerEnvironments(r.Context(), st, p, ref)
	if err != nil {
		return nil, 0, err
	}
	items := []any{}
	service := st.Workers.Workers[ref.Worker]
	for _, env := range out.(map[string]any)["items"].([]domain.WorkerEnvironment) {
		if env.Name == "production" {
			continue
		}
		if ref.Environment != "production" {
			if env.Name == ref.Environment {
				return s.cfPreviewView(service, env), 200, nil
			}
			continue
		}
		items = append(items, s.cfPreviewView(service, env))
	}
	if ref.Environment != "production" {
		return nil, 0, domain.Problem(404, "PREVIEW_NOT_FOUND", "Preview not found")
	}
	return items, 200, nil
}
func (s *Server) cfPreviewDeployments(st *domain.State, p authorization.Principal, r *http.Request) (any, int, error) {
	ref := workerRef(r)
	if _, err := s.WorkerTarget(st, r, ref.Environment, "worker.version.read", p); err != nil {
		return nil, 0, err
	}
	var selected domain.WorkerDeployment
	for _, d := range st.Workers.Deployments {
		if d.Worker != ref.Worker || d.Organization != ref.Organization || d.Environment != ref.Environment || d.Committed == nil {
			continue
		}
		if r.PathValue("deployment") == "latest" {
			if selected.ID == "" || d.Created.After(selected.Created) {
				selected = d
			}
		} else if d.ID == r.PathValue("deployment") {
			selected = d
		}
	}
	if selected.ID == "" {
		return nil, 0, domain.Problem(404, "PREVIEW_DEPLOYMENT_NOT_FOUND", "Preview deployment not found")
	}
	out := cfDeployment(selected)
	version := st.Workers.Versions[selected.Version]
	out["annotations"] = map[string]string{"workers/tag": version.Tag, "workers/message": version.Message}
	out["id"] = selected.ID
	out["env"] = cfEnvBindings(domain.WorkerEnvironment{R2Bindings: st.Workers.Versions[selected.Version].R2Bindings, Variables: st.Workers.Versions[selected.Version].Variables, SecretVersions: st.Workers.Versions[selected.Version].SecretVersions, VersionMetadata: st.Workers.Versions[selected.Version].VersionMetadata, AssetsBinding: st.Workers.Versions[selected.Version].AssetsBinding})
	out["urls"] = s.cfDeploymentURLs(st, selected)
	return out, 200, nil
}
func (s *Server) cfDeploymentURLs(st *domain.State, d domain.WorkerDeployment) []string {
	service, ok := st.Workers.Workers[d.Worker]
	if !ok {
		return []string{}
	}
	service = s.Application(nil).NormalizeWorker(service)
	urls := []string{}
	for _, host := range workerapp.DeploymentHosts(s.Config.Workers, service, d.ID) {
		scheme := "https://"
		if s.Config.Development {
			scheme = "http://"
		}
		urls = append(urls, scheme+host)
	}
	return urls
}

func (s *Server) cfUser(st *domain.State, p authorization.Principal, r *http.Request) (any, int, error) {
	if p.OAuthScope != "" && !strings.Contains(" "+p.OAuthScope+" ", " krun:control ") && !slices.Contains(strings.Fields(p.OAuthScope), "user:read") {
		return nil, 0, domain.Forbidden()
	}
	return map[string]any{"id": p.User.ID, "email": p.User.Email, "first_name": p.User.Name, "last_name": "", "suspended": false}, 200, nil
}

func (s *Server) cfMemberships(st *domain.State, p authorization.Principal, r *http.Request) (any, int, error) {
	out, _, err := s.cfAccounts(st, p, r)
	if err != nil {
		return nil, 0, err
	}
	memberships := []any{}
	for _, org := range out.([]domain.Organization) {
		memberships = append(memberships, map[string]any{"id": p.User.ID + "/" + org.ID, "status": "accepted", "account": map[string]string{"id": org.ID, "name": org.Name}, "roles": s.IAM().MembershipRoles(st, p, org.ID)})
	}
	return memberships, 200, nil
}
