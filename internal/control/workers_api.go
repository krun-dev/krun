package control

import (
	"encoding/json"
	"net/http"
	"sort"
	"time"
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
	m.HandleFunc("POST "+base+"/builds", protectWithBodyLimit(false, workerBuildRequestBodyMax, s.workerBuildCreate))
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
	m.HandleFunc("POST "+base+"/environments/{environment}/releases", protect(false, s.workerPublish))
	m.HandleFunc("POST "+base+"/environments/{environment}/rollbacks", protect(false, s.workerRollback))
	m.HandleFunc("GET "+base+"/environments/{environment}/history", protect(false, s.workerReleaseHistory))
	m.HandleFunc("GET "+base+"/operations", protect(false, s.workerOperationList))
	m.HandleFunc("GET "+base+"/releases/{release}", protect(false, s.workerReleaseGet))
	m.HandleFunc("POST "+base+"/environments/{environment}/invoke", s.workerInvoke)
	m.HandleFunc("GET "+base+"/operations/{operation}", protect(false, s.workerOperationGet))
	m.HandleFunc("GET "+base+"/artifacts/{artifact}", protect(false, s.workerArtifactGet))
}
func (s *Server) workerTarget(st *State, r *http.Request, env, action string, p Principal) (Target, error) {
	t, e := resolve(st, "service", r.PathValue("resource"), env)
	if e != nil || t.Organization != r.PathValue("org") {
		return t, notFound()
	}
	return t, s.require(st, p, r, action, t)
}

// Collection APIs use the union of visible environments, rather than requiring global permission.
func (s *Server) workerCollectionTarget(st *State, r *http.Request, action string, p Principal) (Target, error) {
	t, e := resolve(st, "service", r.PathValue("resource"), "global")
	if e != nil || t.Organization != r.PathValue("org") {
		return t, notFound()
	}
	candidates := []string{workerBaseline}
	if ws, ok := st.Workers.Services[t.ID]; ok {
		names := make([]string, 0, len(ws.Environments))
		for name := range ws.Environments {
			if name != workerBaseline {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		candidates = append(candidates, names...)
	}
	for _, env := range candidates {
		target := t
		target.Environment = env
		d, e := Authorize(st, p, action, target)
		if e != nil {
			return t, e
		}
		if d.Allowed {
			return t, s.require(st, p, r, action, target)
		}
	}
	return t, s.require(st, p, r, action, t)
}
func (s *Server) workerService(st *State, t Target) WorkerService {
	if st.Workers == nil {
		st.Workers = newWorkerState()
	}
	ws, ok := st.Workers.Services[t.ID]
	if !ok {
		ws = WorkerService{Service: t.ID, Organization: t.Organization, Domains: []string{}, Environments: map[string]WorkerEnvironment{}}
	}
	ws.Organization = t.Organization
	ws = s.normalizeService(ws)
	st.Workers.Services[t.ID] = ws
	s.ensureWorkerPlacement(st, ws)
	return ws
}

func (s *Server) ensureWorkerPlacement(st *State, ws WorkerService) WorkerPlacement {
	if placement, ok := activeWorkerPlacement(st.Workers, ws.Service); ok &&
		placement.NetworkProfile == ws.NetworkProfile && s.Config.Workers.poolAllowedForProfile(ws.NetworkProfile, placement.Pool) {
		return placement
	}
	pool, ok := s.Config.Workers.selectPoolForService(ws.NetworkProfile, ws.Service)
	if !ok {
		return WorkerPlacement{}
	}
	return s.replaceWorkerPlacement(st, ws, pool.ID)
}

func (s *Server) replaceWorkerPlacement(st *State, ws WorkerService, pool string) WorkerPlacement {
	for key, placement := range st.Workers.Placements {
		if placement.Service == ws.Service && placement.Role == "primary" {
			delete(st.Workers.Placements, key)
		}
	}
	timestamp := now()
	placement := WorkerPlacement{
		Service: ws.Service, Organization: ws.Organization, NetworkProfile: ws.NetworkProfile,
		Pool: pool, Role: "primary", State: "active", Revision: 1, Created: timestamp, Updated: timestamp,
	}
	st.Workers.Placements[workerPlacementKey(ws.Service, pool)] = placement
	return placement
}

// workerServiceView is the read-only form of workerService: it applies the same
// defaults to the returned value without touching the stored row. Read endpoints
// must use this so a GET never mutates the service row.
//
// The environment map is copied first: normalizeService writes into it in
// place, so returning the stored map by reference would let a read mutate the
// loaded state (and, in the memory store, be observed as a write).
func (s *Server) workerServiceView(st *State, t Target) WorkerService {
	if st.Workers == nil {
		st.Workers = newWorkerState()
	}
	ws, ok := st.Workers.Services[t.ID]
	if !ok {
		ws = WorkerService{Service: t.ID, Organization: t.Organization, Domains: []string{}, Environments: map[string]WorkerEnvironment{}}
	}
	ws.Organization = t.Organization
	ws.Domains = append([]string(nil), ws.Domains...)
	environments := make(map[string]WorkerEnvironment, len(ws.Environments))
	for name, env := range ws.Environments {
		environments[name] = env
	}
	ws.Environments = environments
	return s.normalizeService(ws)
}

// normalizeService applies catalog defaults and derives hosts. Hosts are never
// persisted: routing, the edge projection and API responses all compute them
// here, so they cannot drift apart.
func (s *Server) normalizeService(ws WorkerService) WorkerService {
	ws = s.Config.Workers.normalizeServiceNetwork(ws)
	if ws.Environments == nil {
		ws.Environments = map[string]WorkerEnvironment{}
	}
	// The baseline always exists and always serves production traffic.
	baseline, ok := ws.Environments[workerBaseline]
	if !ok {
		baseline = WorkerEnvironment{ConfigRevision: 1}
	}
	baseline.Name = workerBaseline
	baseline.Enabled = true
	if baseline.ConfigRevision == 0 {
		baseline.ConfigRevision = 1
	}
	if baseline.Variables == nil {
		baseline.Variables = map[string]string{}
	}
	if baseline.SecretVersions == nil {
		baseline.SecretVersions = map[string]string{}
	}
	baseline.SecretNames = workerSecretNames(baseline.SecretVersions)
	ws.Environments[workerBaseline] = baseline
	for name, env := range ws.Environments {
		if env.SecretVersions == nil {
			env.SecretVersions = map[string]string{}
		}
		env.SecretNames = workerSecretNames(env.SecretVersions)
		s.syncEnvironmentHosts(&env, ws.Organization, ws.Service, ws.Domains)
		ws.Environments[name] = env
	}
	return ws
}
func workerIdempotent(st *State, key string, in any) (string, error) {
	if key == "" {
		return "", problem(422, "IDEMPOTENCY_REQUIRED", "An idempotency_key is required")
	}
	raw, _ := json.Marshal(in)
	record, ok := st.Workers.Idempotency[key]
	if !ok {
		return "", nil
	}
	if record.Digest != bytesDigest(raw) {
		return "", problem(409, "IDEMPOTENCY_CONFLICT", "Idempotency key used for another request")
	}
	return record.ID, nil
}
func workerIdempotencyKey(t Target, p Principal, kind, env, key string) string {
	return t.Organization + "/" + t.ID + "/" + p.User.ID + "/" + kind + "/" + env + "/" + key
}
func workerRemember(st *State, key string, in any, id string) {
	raw, _ := json.Marshal(in)
	st.Workers.Idempotency[key] = WorkerIdempotency{bytesDigest(raw), id}
}

// JSON escaping can expand a source byte to six wire bytes (for example, a
// control character encoded as \u00xx). Keep this scoped envelope large enough
// for the full 2 MiB decoded source limit without raising every API's 1 MiB cap.
const workerBuildRequestBodyMax = 16 << 20

func (s *Server) workerBuildCreate(st *State, p Principal, r *http.Request) (any, int, error) {
	var in struct {
		SourceType         string              `json:"source_type"`
		RepositoryRevision *int                `json:"repository_revision"`
		Ref                string              `json:"ref"`
		Environment        string              `json:"environment"`
		Entrypoint         string              `json:"entrypoint"`
		Files              map[string]string   `json:"files"`
		SourceUpload       string              `json:"source_upload"`
		CompatibilityDate  string              `json:"compatibility_date"`
		CompatibilityFlags []string            `json:"compatibility_flags"`
		Assets             *WorkerAssetsConfig `json:"assets,omitempty"`
		Key                string              `json:"idempotency_key"`
	}
	if e := bodyWithLimit(r, &in, workerBuildRequestBodyMax); e != nil {
		return nil, 0, e
	}
	if in.Environment == "" {
		return nil, 0, problem(422, "WORKER_BUILD_INVALID", "environment is required (production or a preview slug)")
	}
	if !workerEnvValid(in.Environment) || in.Key == "" || len(in.Key) > 128 {
		return nil, 0, problem(422, "WORKER_BUILD_INVALID", "Valid environment and idempotency_key required")
	}
	if e := validateWorkerAssets(in.Assets); e != nil {
		return nil, 0, e
	}
	t, e := s.workerTarget(st, r, in.Environment, "build.start", p)
	if e != nil {
		return nil, 0, e
	}
	ws := s.workerService(st, t)
	current, exists := ws.Environments[in.Environment]
	if !exists {
		return nil, 0, notFound()
	}
	if !current.Enabled {
		return nil, 0, problem(409, "ENVIRONMENT_DISABLED", "Target environment is disabled")
	}
	key := workerIdempotencyKey(t, p, "build", in.Environment, in.Key)
	id, e := workerIdempotent(st, key, in)
	if e != nil {
		return nil, 0, e
	}
	if id != "" {
		return st.Workers.Builds[id], 200, nil
	}
	var repository *WorkerBuildRepository
	compatibilityDate := in.CompatibilityDate
	compatibilityFlags := in.CompatibilityFlags
	switch in.SourceType {
	case "repository":
		if in.Assets != nil {
			return nil, 0, problem(422, "WORKER_BUILD_INVALID", "Repository assets are read from the Wrangler configuration")
		}
		if compatibilityDate == "" && len(compatibilityFlags) > 0 {
			return nil, 0, problem(422, "WORKER_COMPATIBILITY_INVALID", "compatibility_flags require an explicit compatibility_date for repository builds")
		}
		if compatibilityDate != "" {
			compatibilityDate, compatibilityFlags, e = normalizeWorkerCompatibility(compatibilityDate, compatibilityFlags)
			if e != nil {
				return nil, 0, problem(422, "WORKER_COMPATIBILITY_INVALID", e.Error())
			}
		}
		if in.RepositoryRevision == nil || *in.RepositoryRevision != ws.RepositoryRevision {
			return nil, 0, problem(409, "REVISION_CONFLICT", "Repository configuration changed")
		}
		if ws.Repository == nil || len(in.Files) != 0 || in.Entrypoint != "" || in.SourceUpload != "" {
			return nil, 0, problem(422, "WORKER_REPOSITORY_INVALID", "Connect a repository before starting a repository build")
		}
		source := *ws.Repository
		if in.Ref != "" {
			source.Ref = in.Ref
		}
		if _, err := s.validateRepositorySource(&source, t.Organization); err != nil {
			return nil, 0, err
		}
		repository = &WorkerBuildRepository{WorkerRepositorySource: source}
	case "", "upload":
		if in.Ref != "" {
			return nil, 0, problem(422, "WORKER_BUILD_INVALID", "A ref is only supported for repository builds")
		}
		if in.SourceUpload != "" {
			// Assets (up to 20 MiB) cannot travel through the JSON request body,
			// so clients upload one source bundle and reference it here.
			if !validWorkerSourceUpload(t.Organization, in.SourceUpload) {
				return nil, 0, problem(422, "WORKER_BUILD_INVALID", "source_upload must reference an uploaded source bundle")
			}
			if len(in.Files) != 0 || in.Entrypoint == "" {
				return nil, 0, problem(422, "WORKER_BUILD_INVALID", "source_upload requires entrypoint and no inline files")
			}
		} else {
			if in.Assets != nil {
				return nil, 0, problem(422, "WORKER_BUILD_INVALID", "assets require a source bundle; upload it first and pass source_upload")
			}
			if err := sourceValid(in.Entrypoint, in.Files); err != nil {
				return nil, 0, err
			}
		}
		compatibilityDate, compatibilityFlags, e = normalizeWorkerCompatibility(compatibilityDate, compatibilityFlags)
		if e != nil {
			return nil, 0, problem(422, "WORKER_COMPATIBILITY_INVALID", e.Error())
		}
	default:
		return nil, 0, problem(422, "WORKER_BUILD_INVALID", "Use repository or upload as source_type")
	}
	count := 0
	for _, b := range st.Workers.Builds {
		if b.Service == t.ID && b.State != "succeeded" && b.State != "failed" {
			count++
		}
	}
	if count >= workerActiveBuildLimit {
		return nil, 0, problem(409, "BUILD_LIMIT", "At most 100 builds may be active per service")
	}
	raw, _ := json.Marshal(in.Files)
	id = randomID("bld")
	job := "krun-build-" + digest(id)[:24]
	if s.Config.Workers.BuilderMode == "local" {
		job = "local-" + digest(id)[:16]
	}
	b := WorkerBuild{ID: id, Organization: t.Organization, Service: t.ID, Environment: in.Environment, State: "queued", Entrypoint: in.Entrypoint, SourceDigest: bytesDigest(raw), Profile: workerProfile, CompatibilityDate: compatibilityDate, CompatibilityFlags: compatibilityFlags, Job: job, Assets: in.Assets, Created: now()}
	if in.SourceUpload != "" {
		b.SourceDigest = ""
	}
	if repository != nil {
		b.Repository = repository
		b.SourceDigest = ""
	}
	st.Workers.Builds[id] = b
	st.Workers.BuildPrivate[id] = WorkerBuildPrivate{Actor: workerActor(p), Files: in.Files, SourceKey: in.SourceUpload}
	workerRemember(st, key, in, id)
	return b, 202, nil
}

// Clients upload a source bundle (src/ + assets/) with the returned URL, then
// reference the key in POST /builds. Signing is offline; the upload goes
// directly to object storage.
func (s *Server) workerSourceUploadCreate(st *State, p Principal, r *http.Request) (any, int, error) {
	t, e := s.workerCollectionTarget(st, r, "build.start", p)
	if e != nil {
		return nil, 0, e
	}
	key, url, e := s.workers.sourceUploadURL(r.Context(), t.Organization)
	if e != nil {
		return nil, 0, problem(503, "SOURCE_UPLOAD_UNAVAILABLE", "Source storage unavailable")
	}
	return map[string]any{"source_upload": key, "upload_url": url, "expires_in": 900}, 201, nil
}
func (s *Server) workerBuildList(st *State, p Principal, r *http.Request) (any, int, error) {
	t, e := s.workerCollectionTarget(st, r, "service.version.read", p)
	if e != nil {
		return nil, 0, e
	}
	out := []WorkerBuild{}
	for _, b := range st.Workers.Builds {
		if b.Service == t.ID {
			d, e := Authorize(st, p, "service.version.read", Target{Kind: "service", ID: t.ID, Organization: t.Organization, Environment: b.Environment})
			if e != nil {
				return nil, 0, e
			}
			if d.Allowed {
				out = append(out, b)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return items(out)
}
func (s *Server) workerGetBuild(st *State, p Principal, r *http.Request, action string) (WorkerBuild, error) {
	b, ok := st.Workers.Builds[r.PathValue("build")]
	if !ok || b.Service != r.PathValue("resource") || b.Organization != r.PathValue("org") {
		return b, notFound()
	}
	_, e := s.workerTarget(st, r, b.Environment, action, p)
	return b, e
}
func (s *Server) workerBuildGet(st *State, p Principal, r *http.Request) (any, int, error) {
	b, e := s.workerGetBuild(st, p, r, "service.version.read")
	return b, 200, e
}
func (s *Server) workerBuildLogs(st *State, p Principal, r *http.Request) (any, int, error) {
	b, e := s.workerGetBuild(st, p, r, "log.read")
	if e != nil {
		return nil, 0, e
	}
	private, ok, e := s.Store.workerBuildPrivate(r.Context(), b.ID)
	if e != nil {
		return nil, 0, e
	}
	if !ok {
		private = WorkerBuildPrivate{}
	}
	return map[string]any{"build_id": b.ID, "state": b.State, "log": private.Log}, 200, nil
}
func (s *Server) workerBuildSource(st *State, p Principal, r *http.Request) (any, int, error) {
	b, e := s.workerGetBuild(st, p, r, "source.read")
	if e != nil {
		return nil, 0, e
	}
	private, ok, e := s.Store.workerBuildPrivate(r.Context(), b.ID)
	if e != nil {
		return nil, 0, e
	}
	if !ok {
		private = WorkerBuildPrivate{}
	}
	if private.SourceKey != "" {
		url, e := s.workers.sourceBundleURL(r.Context(), private.SourceKey)
		if e != nil {
			return nil, 0, problem(503, "SOURCE_UNAVAILABLE", "Source storage unavailable")
		}
		return map[string]any{"entrypoint": b.Entrypoint, "download_url": url}, 200, nil
	}
	return map[string]any{"entrypoint": b.Entrypoint, "files": private.Files}, 200, nil
}
func (s *Server) workerEnvironments(st *State, p Principal, r *http.Request) (any, int, error) {
	t, e := s.workerCollectionTarget(st, r, "service.read", p)
	if e != nil {
		return nil, 0, e
	}
	ws := s.workerServiceView(st, t)
	names := make([]string, 0, len(ws.Environments))
	for name := range ws.Environments {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		if names[i] == workerBaseline {
			return true
		}
		if names[j] == workerBaseline {
			return false
		}
		return names[i] < names[j]
	})
	out := []WorkerEnvironment{}
	for _, name := range names {
		d, e := Authorize(st, p, "service.read", Target{Kind: "service", ID: t.ID, Organization: t.Organization, Environment: name})
		if e != nil {
			return nil, 0, e
		}
		if d.Allowed {
			env := ws.Environments[name]
			// Recompute rather than trusting persisted hosts: the configured
			// domain catalog may have changed since the row was written.
			s.syncEnvironmentHosts(&env, t.Organization, t.ID, ws.Domains)
			out = append(out, env)
		}
	}
	return items(out)
}

type workerOverviewEnvironment struct {
	Environment      WorkerEnvironment `json:"environment"`
	History          []WorkerHistory   `json:"history"`
	CurrentRelease   *WorkerRelease    `json:"current_release,omitempty"`
	ConfirmedRelease *WorkerRelease    `json:"confirmed_release,omitempty"`
}

func publicWorkerRelease(release WorkerRelease) WorkerRelease {
	return release
}

// workerOverview is the console polling read model. It deliberately contains
// only public Workers fields and applies the same per-environment permissions
// as the individual collection endpoints.
func (s *Server) workerOverview(st *State, p Principal, r *http.Request) (any, int, error) {
	t, err := s.workerCollectionTarget(st, r, "service.read", p)
	if err != nil {
		return nil, 0, err
	}
	ws := s.workerServiceView(st, t)
	names := make([]string, 0, len(ws.Environments))
	for name := range ws.Environments {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		if names[i] == workerBaseline {
			return true
		}
		if names[j] == workerBaseline {
			return false
		}
		return names[i] < names[j]
	})

	environments := []workerOverviewEnvironment{}
	versionVisible := map[string]bool{}
	releaseIDs := map[string]bool{}
	for _, name := range names {
		target := Target{Kind: "service", ID: t.ID, Organization: t.Organization, Environment: name}
		read, authErr := Authorize(st, p, "service.read", target)
		if authErr != nil {
			return nil, 0, authErr
		}
		if !read.Allowed {
			continue
		}
		env := ws.Environments[name]
		s.syncEnvironmentHosts(&env, t.Organization, t.ID, ws.Domains)
		entry := workerOverviewEnvironment{Environment: env, History: []WorkerHistory{}}
		versions, authErr := Authorize(st, p, "service.version.read", target)
		if authErr != nil {
			return nil, 0, authErr
		}
		if versions.Allowed {
			versionVisible[name] = true
			entry.History = append(entry.History, st.Workers.Histories[t.ID+"/"+name]...)
			for _, history := range entry.History {
				releaseIDs[history.Release] = true
			}
			if release, ok := st.Workers.Releases[env.Release]; ok {
				value := publicWorkerRelease(release)
				entry.CurrentRelease = &value
				releaseIDs[release.ID] = true
			}
			if release, ok := st.Workers.Releases[env.Confirmed]; ok {
				value := publicWorkerRelease(release)
				entry.ConfirmedRelease = &value
				releaseIDs[release.ID] = true
			}
		}
		environments = append(environments, entry)
	}

	builds := []WorkerBuild{}
	for _, build := range st.Workers.Builds {
		if build.Service == t.ID && build.Organization == t.Organization && versionVisible[build.Environment] {
			builds = append(builds, build)
		}
	}
	sort.Slice(builds, func(i, j int) bool { return builds[i].Created.After(builds[j].Created) })

	operations := []WorkerOperation{}
	for _, operation := range st.Workers.Operations {
		if operation.Service == t.ID && operation.Organization == t.Organization && versionVisible[operation.Environment] {
			operations = append(operations, operation)
		}
	}
	sort.Slice(operations, func(i, j int) bool { return operations[i].Created.After(operations[j].Created) })
	if len(operations) > 100 {
		operations = operations[:100]
	}
	releases := map[string]WorkerRelease{}
	for id := range releaseIDs {
		if release, ok := st.Workers.Releases[id]; ok && release.Service == t.ID && release.Organization == t.Organization && versionVisible[release.Environment] {
			releases[id] = publicWorkerRelease(release)
		}
	}
	return map[string]any{
		"builds": builds, "environments": environments, "operations": operations, "releases": releases,
		"domains_revision": ws.DomainsRevision,
	}, 200, nil
}

// workerServiceDomains returns the service-level domain selection together with
// the catalog, so the console can render the routing page in one round trip.
func (s *Server) workerServiceDomains(st *State, p Principal, r *http.Request) (any, int, error) {
	t, e := s.workerTarget(st, r, workerBaseline, "service.read", p)
	if e != nil {
		return nil, 0, e
	}
	ws := s.workerServiceView(st, t)
	profile, _ := s.Config.Workers.networkProfile(ws.NetworkProfile)
	return map[string]any{
		"selected": s.syncServiceDomains(ws), "catalog": s.domainCatalogForProfile(profile), "revision": ws.DomainsRevision,
		"network_profile": WorkerNetworkProfileView{ID: profile.ID, Name: profile.Name, Description: profile.Description, ApprovalRequired: profile.Approval == "required", Default: profile.Default},
	}, 200, nil
}

// workerServiceDomainsUpdate replaces the service-level domain selection. Every
// environment of the service is re-homed onto the new set.
func (s *Server) workerServiceDomainsUpdate(st *State, p Principal, r *http.Request) (any, int, error) {
	t, e := s.workerTarget(st, r, workerBaseline, "service.update", p)
	if e != nil {
		return nil, 0, e
	}
	var in struct {
		Domains          []string `json:"domains"`
		ExpectedRevision *int     `json:"expected_revision"`
	}
	if e = body(r, &in); e != nil {
		return nil, 0, e
	}
	if len(in.Domains) == 0 {
		return nil, 0, problem(422, "DOMAINS_INVALID", "Select at least one domain")
	}
	ws := s.workerService(st, t)
	profile, ok := s.Config.Workers.networkProfile(ws.NetworkProfile)
	if !ok {
		return nil, 0, problem(409, "WORKER_NETWORK_PROFILE_INVALID", "The service network profile is no longer available")
	}
	allowed := map[string]bool{}
	for _, domain := range s.Config.Workers.domainsForProfile(profile) {
		allowed[domain.ID] = true
	}
	seen := map[string]bool{}
	for _, id := range in.Domains {
		if !allowed[id] {
			return nil, 0, problem(422, "DOMAINS_INVALID", "Domain is not allowed by the selected network profile: "+id)
		}
		if seen[id] {
			return nil, 0, problem(422, "DOMAINS_INVALID", "Duplicate domain: "+id)
		}
		seen[id] = true
	}
	if in.ExpectedRevision == nil {
		return nil, 0, problem(422, "DOMAINS_INVALID", "expected_revision is required")
	}
	if *in.ExpectedRevision != ws.DomainsRevision {
		return nil, 0, problem(409, "REVISION_CONFLICT", "Domain configuration changed")
	}
	ws.Domains = append([]string(nil), in.Domains...)
	ws.DomainsRevision++
	selected := s.syncServiceHosts(&ws)
	st.Workers.Services[t.ID] = ws
	s.audit(st, p, r, "workers.domains.update", t.ID, true)
	return map[string]any{"selected": selected, "catalog": s.domainCatalogForProfile(profile), "revision": ws.DomainsRevision}, 200, nil
}

func (s *Server) workerServiceNetworkProfileUpdate(st *State, p Principal, r *http.Request) (any, int, error) {
	t, err := s.workerTarget(st, r, workerBaseline, "service.update", p)
	if err != nil {
		return nil, 0, err
	}
	var in struct {
		Profile          string `json:"network_profile"`
		ExpectedRevision *int   `json:"expected_revision"`
	}
	if err = body(r, &in); err != nil {
		return nil, 0, err
	}
	if in.ExpectedRevision == nil {
		return nil, 0, problem(422, "NETWORK_PROFILE_INVALID", "expected_revision is required")
	}
	profile, ok := s.Config.Workers.networkProfile(in.Profile)
	if !ok || profile.Visibility != "public" {
		return nil, 0, problem(404, "NETWORK_PROFILE_NOT_FOUND", "Network profile not found")
	}
	if profile.Approval == "required" {
		return nil, 0, problem(403, "NETWORK_PROFILE_APPROVAL_REQUIRED", "This network profile requires platform approval")
	}
	pool, ok := s.Config.Workers.selectPoolForService(profile.ID, t.ID)
	if !ok {
		return nil, 0, problem(409, "NETWORK_PROFILE_UNAVAILABLE", "The selected network profile has no available runtime placement")
	}
	ws := s.workerService(st, t)
	if ws.DomainsRevision != *in.ExpectedRevision {
		return nil, 0, problem(409, "REVISION_CONFLICT", "Network configuration changed")
	}
	if ws.NetworkProfile == profile.ID {
		return s.workerServiceDomains(st, p, r)
	}
	for _, env := range ws.Environments {
		if env.Release != "" {
			return nil, 0, problem(409, "WORKER_NETWORK_REASSIGNMENT_REQUIRED", "Published Workers require a platform-managed runtime reassignment before changing network profile")
		}
	}
	ws.NetworkProfile = profile.ID
	ws.Domains = s.Config.Workers.defaultDomainsForProfile(profile)
	ws.DomainsRevision++
	selected := s.syncServiceHosts(&ws)
	st.Workers.Services[t.ID] = ws
	s.replaceWorkerPlacement(st, ws, pool.ID)
	s.audit(st, p, r, "workers.network-profile.update", t.ID, true)
	return map[string]any{
		"selected": selected, "catalog": s.domainCatalogForProfile(profile), "revision": ws.DomainsRevision,
		"network_profile": WorkerNetworkProfileView{ID: profile.ID, Name: profile.Name, Description: profile.Description, ApprovalRequired: false, Default: profile.Default},
	}, 200, nil
}
func workerBusy(st *State, service, env string) bool {
	for _, op := range st.Workers.Operations {
		// A committed operation is already durable and converging; a new
		// publish or config change supersedes it instead of blocking.
		if op.Service == service && op.Environment == env && !workerTerminal(op.State) && op.State != "committed" {
			return true
		}
	}
	return false
}

// Previews are created explicitly; the durable baseline always exists and cannot be created or deleted.
func (s *Server) workerEnvironmentCreate(st *State, p Principal, r *http.Request) (any, int, error) {
	var in struct {
		Name string `json:"name"`
	}
	if e := body(r, &in); e != nil {
		return nil, 0, e
	}
	if !workerPreviewValid(in.Name) {
		return nil, 0, problem(422, "ENVIRONMENT_INVALID", "Preview name must be a slug and cannot be production")
	}
	t, e := s.workerTarget(st, r, in.Name, "service.update", p)
	if e != nil {
		return nil, 0, e
	}
	ws := s.workerService(st, t)
	if _, exists := ws.Environments[in.Name]; exists {
		return nil, 0, problem(409, "ENVIRONMENT_EXISTS", "Preview environment already exists")
	}
	previews := 0
	for name := range ws.Environments {
		if name != workerBaseline {
			previews++
		}
	}
	limit := s.Config.Workers.PreviewLimit
	if limit <= 0 {
		limit = 32
	}
	if previews >= limit {
		return nil, 0, problem(409, "ENVIRONMENT_LIMIT", "Too many preview environments")
	}
	env := WorkerEnvironment{Name: in.Name, Enabled: true, ConfigRevision: 1, Variables: map[string]string{}, SecretVersions: map[string]string{}}
	s.syncEnvironmentHosts(&env, t.Organization, t.ID, ws.Domains)
	ws.Environments[in.Name] = env
	ws.PreviewRevision++
	st.Workers.Services[t.ID] = ws
	s.audit(st, p, r, "workers.environment.create", t.ID+"/"+in.Name, true)
	return env, 201, nil
}
func (s *Server) workerEnvironmentDelete(st *State, p Principal, r *http.Request) (any, int, error) {
	env := r.PathValue("environment")
	if env == workerBaseline || !workerEnvValid(env) {
		return nil, 0, notFound()
	}
	t, e := s.workerTarget(st, r, env, "service.update", p)
	if e != nil {
		return nil, 0, e
	}
	ws := s.workerService(st, t)
	if _, exists := ws.Environments[env]; !exists {
		return nil, 0, notFound()
	}
	if workerBusy(st, t.ID, env) {
		return nil, 0, problem(409, "RELEASE_BUSY", "Wait for the active release operation")
	}
	delete(ws.Environments, env)
	ws.PreviewRevision++
	st.Workers.Services[t.ID] = ws
	s.audit(st, p, r, "workers.environment.delete", t.ID+"/"+env, true)
	return nil, 204, nil
}
func (s *Server) workerEnvironmentUpdate(st *State, p Principal, r *http.Request) (any, int, error) {
	env := r.PathValue("environment")
	if !workerEnvValid(env) {
		return nil, 0, notFound()
	}
	t, e := s.workerTarget(st, r, env, "service.update", p)
	if e != nil {
		return nil, 0, e
	}
	var in struct {
		Revision  int               `json:"expected_revision"`
		Enabled   *bool             `json:"enabled"`
		Variables map[string]string `json:"variables"`
	}
	if e = body(r, &in); e != nil {
		return nil, 0, e
	}
	ws := s.workerService(st, t)
	current, exists := ws.Environments[env]
	if !exists {
		return nil, 0, notFound()
	}
	if current.ConfigRevision != in.Revision {
		return nil, 0, problem(409, "REVISION_CONFLICT", "Environment configuration changed")
	}
	if workerBusy(st, t.ID, env) {
		return nil, 0, problem(409, "RELEASE_BUSY", "Wait for the active release operation")
	}
	if in.Variables != nil && !workerVariablesValid(in.Variables) {
		return nil, 0, problem(422, "WORKER_VARIABLES_INVALID", "Use up to 32 non-reserved uppercase variables, at most 16 KiB")
	}
	if in.Variables != nil {
		for name := range in.Variables {
			if _, exists := current.SecretVersions[name]; exists {
				return nil, 0, problem(409, "WORKER_BINDING_CONFLICT", "A Secret already uses this binding name")
			}
		}
	}
	if in.Enabled != nil {
		if env == workerBaseline && !*in.Enabled {
			return nil, 0, problem(422, "ENVIRONMENT_INVALID", "The baseline environment cannot be disabled")
		}
		current.Enabled = *in.Enabled
	}
	if in.Variables != nil {
		current.Variables = in.Variables
	}
	s.syncEnvironmentHosts(&current, t.Organization, t.ID, ws.Domains)
	current.ConfigRevision++
	ws.Environments[env] = current
	st.Workers.Services[t.ID] = ws
	return current, 200, nil
}
func (s *Server) workerPublish(st *State, p Principal, r *http.Request) (any, int, error) {
	return s.workerReleaseCreate(st, p, r, "publish")
}
func (s *Server) workerRollback(st *State, p Principal, r *http.Request) (any, int, error) {
	return s.workerReleaseCreate(st, p, r, "rollback")
}
func (s *Server) workerReleaseCreate(st *State, p Principal, r *http.Request, kind string) (any, int, error) {
	env := r.PathValue("environment")
	if !workerEnvValid(env) {
		return nil, 0, notFound()
	}
	t, e := s.workerTarget(st, r, env, "service."+kind, p)
	if e != nil {
		return nil, 0, e
	}
	var in struct {
		Build    string `json:"build_id,omitempty"`
		Release  string `json:"release_id,omitempty"`
		Revision int    `json:"expected_revision"`
		Key      string `json:"idempotency_key"`
	}
	if e = body(r, &in); e != nil {
		return nil, 0, e
	}
	if in.Key == "" || len(in.Key) > 128 || in.Revision < 0 {
		return nil, 0, problem(422, "WORKER_RELEASE_INVALID", "expected_revision and idempotency_key required")
	}
	ws := s.workerService(st, t)
	current := ws.Environments[env]
	key := workerIdempotencyKey(t, p, kind, env, in.Key)
	id, e := workerIdempotent(st, key, in)
	if e != nil {
		return nil, 0, e
	}
	if id != "" {
		return st.Workers.Operations[id], 200, nil
	}
	current, exists := ws.Environments[env]
	if !exists {
		return nil, 0, notFound()
	}
	if !current.Enabled {
		return nil, 0, problem(409, "ENVIRONMENT_DISABLED", "Target environment is disabled")
	}
	if current.Revision != in.Revision {
		return nil, 0, problem(409, "REVISION_CONFLICT", "Environment release changed")
	}
	for _, active := range st.Workers.Operations {
		// Cancellation is only possible before commit; a committed release is
		// durable and may be replaced by a newer publish.
		if active.Service == t.ID && active.Environment == env && !workerTerminal(active.State) && active.State != "committed" {
			return nil, 0, problem(409, "RELEASE_BUSY", "Wait for the active release operation")
		}
	}
	var release WorkerRelease
	if kind == "publish" {
		if in.Build == "" || in.Release != "" {
			return nil, 0, problem(422, "WORKER_RELEASE_INVALID", "Publish requires build_id only")
		}
		b, ok := st.Workers.Builds[in.Build]
		if !ok || b.Service != t.ID || b.Organization != t.Organization {
			return nil, 0, notFound()
		}
		if b.State != "succeeded" {
			return nil, 0, problem(409, "BUILD_NOT_READY", "Build must have a finalized artifact")
		}
		a, ok := st.Workers.Artifacts[b.Artifact]
		if !ok || a.State != "finalized" {
			return nil, 0, problem(409, "ARTIFACT_UNAVAILABLE", "Artifact unavailable")
		}
		release = WorkerRelease{Service: t.ID, Organization: t.Organization, Environment: env, Artifact: a.ID, Profile: a.Profile, ArchiveDigest: a.ArchiveDigest, ModuleDigest: a.ModuleDigest, CompatibilityDate: a.CompatibilityDate, CompatibilityFlags: append([]string(nil), a.CompatibilityFlags...), ConfigRevision: current.ConfigRevision, Variables: current.Variables, SecretDigest: workerSecretsDigest(current.SecretVersions), SecretVersions: cloneStringsMap(current.SecretVersions)}
		raw, _ := json.Marshal(release)
		release.ID = "rel_" + bytesDigest(raw)
		release.Created = now()
		if old, ok := st.Workers.Releases[release.ID]; ok {
			release = old
		}
		st.Workers.Releases[release.ID] = release
	} else {
		if in.Release == "" || in.Build != "" {
			return nil, 0, problem(422, "WORKER_RELEASE_INVALID", "Rollback requires release_id only")
		}
		d, ok := st.Workers.Releases[in.Release]
		if !ok || d.Service != t.ID || d.Organization != t.Organization || d.Environment != env {
			return nil, 0, notFound()
		}
		historical := false
		for _, h := range st.Workers.Histories[t.ID+"/"+env] {
			if h.Release == d.ID {
				historical = true
			}
		}
		if !historical {
			return nil, 0, problem(409, "ROLLBACK_NOT_PUBLISHED", "Rollback target must be a successfully published release")
		}
		release = d
	}
	id = randomID("op")
	op := WorkerOperation{ID: id, Kind: kind, Organization: t.Organization, Service: t.ID, Environment: env, State: "queued", Build: in.Build, Release: release.ID, ExpectedRevision: current.Revision, ConfigRevision: current.ConfigRevision, Steps: []WorkerStep{{"artifact", "pending", ""}, {"candidate", "pending", ""}, {"commit", "pending", ""}, {"synchronize", "pending", ""}}, Created: now()}
	st.Workers.Operations[id] = op
	st.Workers.OperationPrivate[id] = WorkerOperationPrivate{workerActor(p)}
	workerRemember(st, key, in, id)
	return op, 202, nil
}
func (s *Server) workerOperationGet(st *State, p Principal, r *http.Request) (any, int, error) {
	op, ok := st.Workers.Operations[r.PathValue("operation")]
	if !ok || op.Service != r.PathValue("resource") || op.Organization != r.PathValue("org") {
		return nil, 0, notFound()
	}
	_, e := s.workerTarget(st, r, op.Environment, "service.read", p)
	return op, 200, e
}
func (s *Server) workerReleaseHistory(st *State, p Principal, r *http.Request) (any, int, error) {
	env := r.PathValue("environment")
	if !workerEnvValid(env) {
		return nil, 0, notFound()
	}
	t, e := s.workerTarget(st, r, env, "service.version.read", p)
	if e != nil {
		return nil, 0, e
	}
	out := st.Workers.Histories[t.ID+"/"+env]
	if out == nil {
		out = []WorkerHistory{}
	}
	return items(out)
}
func (s *Server) workerArtifactGet(st *State, p Principal, r *http.Request) (any, int, error) {
	t, e := s.workerCollectionTarget(st, r, "service.version.read", p)
	if e != nil {
		return nil, 0, e
	}
	a, ok := st.Workers.Artifacts[r.PathValue("artifact")]
	related := false
	for _, b := range st.Workers.Builds {
		if b.Service == t.ID && b.Artifact == a.ID {
			target := t
			target.Environment = b.Environment
			d, e := Authorize(st, p, "service.version.read", target)
			if e != nil {
				return nil, 0, e
			}
			related = related || d.Allowed
		}
	}
	if !ok || a.Organization != t.Organization || !related {
		return nil, 0, notFound()
	}
	return a, 200, nil
}

// Reauthorize durable operations with current user/Key state before each privileged transition.
func (s *Server) workerAuthorizeActor(st *State, a WorkerActor, service, env, action string) error {
	user, ok := st.Users[a.User]
	if !ok || user.Status != "active" || user.AuthVersion != a.AuthVersion || !s.sourceEnabled(user.Source) {
		return forbidden()
	}
	p := Principal{User: user}
	if a.Key != "" {
		key, ok := st.Keys[a.Key]
		if !ok || !key.active() || key.Owner != a.User {
			return forbidden()
		}
		p.Key = &key
	}
	target, e := resolve(st, "service", service, env)
	if e != nil {
		return e
	}
	d, e := Authorize(st, p, action, target)
	if e != nil {
		return e
	}
	if !d.Allowed {
		return forbidden()
	}
	return nil
}
func workerFailOperation(st *State, id, message string) {
	op, ok := st.Workers.Operations[id]
	if !ok || workerTerminal(op.State) {
		return
	}
	op.State = "failed"
	op.Error = message
	at := now()
	op.Finished = &at
	for i := range op.Steps {
		if op.Steps[i].State == "running" {
			op.Steps[i].State = "failed"
			op.Steps[i].Detail = message
		}
	}
	st.Workers.Operations[id] = op
}
func workerCloneTime(t time.Time) *time.Time { return &t }

func (s *Server) workerOperationList(st *State, p Principal, r *http.Request) (any, int, error) {
	t, e := s.workerCollectionTarget(st, r, "service.version.read", p)
	if e != nil {
		return nil, 0, e
	}
	out := []WorkerOperation{}
	for _, op := range st.Workers.Operations {
		if op.Service != t.ID || op.Organization != t.Organization {
			continue
		}
		target := t
		target.Environment = op.Environment
		d, e := Authorize(st, p, "service.version.read", target)
		if e != nil {
			return nil, 0, e
		}
		if d.Allowed {
			out = append(out, op)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	if len(out) > 100 {
		out = out[:100]
	}
	return items(out)
}
func (s *Server) workerReleaseGet(st *State, p Principal, r *http.Request) (any, int, error) {
	d, ok := st.Workers.Releases[r.PathValue("release")]
	if !ok || d.Service != r.PathValue("resource") || d.Organization != r.PathValue("org") {
		return nil, 0, notFound()
	}
	_, e := s.workerTarget(st, r, d.Environment, "service.version.read", p)
	return publicWorkerRelease(d), 200, e
}
