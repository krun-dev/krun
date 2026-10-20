package control

import (
	"encoding/json"
	"net/http"
	"sort"
	"time"
)

func (s *Server) workersRoutes(m *http.ServeMux) {
	if s.Config.Workers == nil {
		return
	}
	base := "/api/v1/orgs/{org}/services/{resource}/workers"
	m.HandleFunc("GET /api/v1/orgs/{org}/workers/services", s.protected(false, s.workerServiceList))
	m.HandleFunc("GET /api/v1/orgs/{org}/workers/repository-providers", s.protected(false, s.workerRepositoryProviders))
	m.HandleFunc("POST /api/v1/orgs/{org}/workers/repository/{provider}/connect", s.protected(true, s.workerRepositoryConnect))
	m.HandleFunc("POST /api/v1/orgs/{org}/workers/repository/{provider}/disconnect", s.protected(true, s.workerRepositoryDisconnect))
	m.HandleFunc("POST /api/v1/workers/repository/{provider}/callback", s.workerRepositoryCallback)
	m.HandleFunc("GET "+base+"/repository", s.protected(false, s.workerRepositoryGet))
	m.HandleFunc("PUT "+base+"/repository", s.protected(false, s.workerRepositoryUpdate))
	m.HandleFunc("POST "+base+"/builds", s.protected(false, s.workerBuildCreate))
	m.HandleFunc("GET "+base+"/builds", s.protected(false, s.workerBuildList))
	m.HandleFunc("GET "+base+"/builds/{build}", s.protected(false, s.workerBuildGet))
	m.HandleFunc("GET "+base+"/builds/{build}/logs", s.protected(false, s.workerBuildLogs))
	m.HandleFunc("GET "+base+"/builds/{build}/source", s.protected(false, s.workerBuildSource))
	m.HandleFunc("GET "+base+"/environments", s.protected(false, s.workerEnvironments))
	m.HandleFunc("POST "+base+"/environments", s.protected(false, s.workerEnvironmentCreate))
	m.HandleFunc("DELETE "+base+"/environments/{environment}", s.protected(false, s.workerEnvironmentDelete))
	m.HandleFunc("PATCH "+base+"/environments/{environment}", s.protected(false, s.workerEnvironmentUpdate))
	m.HandleFunc("POST "+base+"/environments/{environment}/releases", s.protected(false, s.workerPublish))
	m.HandleFunc("POST "+base+"/environments/{environment}/rollbacks", s.protected(false, s.workerRollback))
	m.HandleFunc("GET "+base+"/environments/{environment}/history", s.protected(false, s.workerReleaseHistory))
	m.HandleFunc("GET "+base+"/operations", s.protected(false, s.workerOperationList))
	m.HandleFunc("GET "+base+"/deployments/{deployment}", s.protected(false, s.workerDeploymentGet))
	m.HandleFunc("POST "+base+"/environments/{environment}/invoke", s.workerInvoke)
	m.HandleFunc("GET "+base+"/operations/{operation}", s.protected(false, s.workerOperationGet))
	m.HandleFunc("GET "+base+"/artifacts/{artifact}", s.protected(false, s.workerArtifactGet))
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
		ws = WorkerService{Service: t.ID, Organization: t.Organization, Environments: map[string]WorkerEnvironment{}}
	}
	if _, ok := ws.Environments[workerBaseline]; !ok {
		ws.Environments[workerBaseline] = WorkerEnvironment{Name: workerBaseline, Enabled: true, Host: "w-" + digest(t.Organization + "/" + t.ID)[:16] + "-" + workerBaseline + "." + s.Config.Workers.Domain, Visibility: "private", ConfigRevision: 1, Variables: map[string]string{}}
	}
	st.Workers.Services[t.ID] = ws
	st.Workers.Revision++
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
func (s *Server) workerBuildCreate(st *State, p Principal, r *http.Request) (any, int, error) {
	var in struct {
		SourceType         string            `json:"source_type"`
		RepositoryRevision *int              `json:"repository_revision"`
		Ref                string            `json:"ref"`
		Environment        string            `json:"environment"`
		Entrypoint         string            `json:"entrypoint"`
		Files              map[string]string `json:"files"`
		Key                string            `json:"idempotency_key"`
	}
	if e := body(r, &in); e != nil {
		return nil, 0, e
	}
	if in.Environment == "" {
		return nil, 0, problem(422, "WORKER_BUILD_INVALID", "environment is required (production or a preview slug)")
	}
	if !workerEnvValid(in.Environment) || in.Key == "" || len(in.Key) > 128 {
		return nil, 0, problem(422, "WORKER_BUILD_INVALID", "Valid environment and idempotency_key required")
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
	switch in.SourceType {
	case "repository":
		if in.RepositoryRevision == nil || *in.RepositoryRevision != ws.RepositoryRevision {
			return nil, 0, problem(409, "REVISION_CONFLICT", "Repository configuration changed")
		}
		if ws.Repository == nil || len(in.Files) != 0 || in.Entrypoint != "" {
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
		if err := sourceValid(in.Entrypoint, in.Files); err != nil {
			return nil, 0, err
		}
	default:
		return nil, 0, problem(422, "WORKER_BUILD_INVALID", "Use repository or upload as source_type")
	}
	count := 0
	for _, b := range st.Workers.Builds {
		if b.Service == t.ID {
			count++
		}
	}
	if count >= 100 {
		return nil, 0, problem(409, "BUILD_LIMIT", "MVP retains at most 100 builds per service")
	}
	raw, _ := json.Marshal(in.Files)
	id = randomID("bld")
	b := WorkerBuild{ID: id, Organization: t.Organization, Service: t.ID, Environment: in.Environment, State: "queued", Entrypoint: in.Entrypoint, SourceDigest: bytesDigest(raw), Profile: workerProfile, Job: "krun-build-" + digest(id)[:24], Created: now()}
	if repository != nil {
		b.Repository = repository
		b.Entrypoint = repository.Entrypoint
		b.SourceDigest = ""
	}
	st.Workers.Builds[id] = b
	st.Workers.BuildPrivate[id] = WorkerBuildPrivate{Actor: workerActor(p), Files: in.Files}
	workerRemember(st, key, in, id)
	return b, 202, nil
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
	return map[string]any{"build_id": b.ID, "state": b.State, "log": st.Workers.BuildPrivate[b.ID].Log}, 200, nil
}
func (s *Server) workerBuildSource(st *State, p Principal, r *http.Request) (any, int, error) {
	b, e := s.workerGetBuild(st, p, r, "source.read")
	if e != nil {
		return nil, 0, e
	}
	return map[string]any{"entrypoint": b.Entrypoint, "files": st.Workers.BuildPrivate[b.ID].Files}, 200, nil
}
func (s *Server) workerEnvironments(st *State, p Principal, r *http.Request) (any, int, error) {
	t, e := s.workerCollectionTarget(st, r, "service.read", p)
	if e != nil {
		return nil, 0, e
	}
	ws := s.workerService(st, t)
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
			out = append(out, ws.Environments[name])
		}
	}
	return items(out)
}
func workerBusy(st *State, service, env string) bool {
	for _, op := range st.Workers.Operations {
		if op.Service == service && op.Environment == env && !workerTerminal(op.State) {
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
	env := WorkerEnvironment{Name: in.Name, Enabled: true, Host: "w-" + digest(t.Organization+"/"+t.ID)[:16] + "-" + in.Name + "." + s.Config.Workers.Domain, Visibility: "private", ConfigRevision: 1, Variables: map[string]string{}}
	ws.Environments[in.Name] = env
	st.Workers.Services[t.ID] = ws
	st.Workers.Revision++
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
	st.Workers.Services[t.ID] = ws
	st.Workers.Revision++
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
	if in.Enabled != nil {
		current.Enabled = *in.Enabled
	}
	if in.Variables != nil {
		current.Variables = in.Variables
	}
	current.ConfigRevision++
	ws.Environments[env] = current
	st.Workers.Services[t.ID] = ws
	st.Workers.Revision++
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
		Build      string `json:"build_id,omitempty"`
		Deployment string `json:"deployment_id,omitempty"`
		Revision   int    `json:"expected_revision"`
		Key        string `json:"idempotency_key"`
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
		if active.Service == t.ID && active.Environment == env && !workerTerminal(active.State) && !(kind == "rollback" && active.State == "committed") {
			return nil, 0, problem(409, "RELEASE_BUSY", "Wait for the active release operation")
		}
	}
	var deployment WorkerDeployment
	if kind == "publish" {
		if in.Build == "" || in.Deployment != "" {
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
		deployment = WorkerDeployment{Service: t.ID, Organization: t.Organization, Environment: env, Artifact: a.ID, ArchiveDigest: a.ArchiveDigest, ModuleDigest: a.ModuleDigest, ConfigRevision: current.ConfigRevision, Variables: current.Variables, Pool: "default"}
		raw, _ := json.Marshal(deployment)
		deployment.ID = "dep_" + bytesDigest(raw)
		deployment.Created = now()
		if old, ok := st.Workers.Deployments[deployment.ID]; ok {
			deployment = old
		}
		st.Workers.Deployments[deployment.ID] = deployment
	} else {
		if in.Deployment == "" || in.Build != "" {
			return nil, 0, problem(422, "WORKER_RELEASE_INVALID", "Rollback requires deployment_id only")
		}
		d, ok := st.Workers.Deployments[in.Deployment]
		if !ok || d.Service != t.ID || d.Organization != t.Organization || d.Environment != env {
			return nil, 0, notFound()
		}
		historical := false
		for _, h := range st.Workers.Histories[t.ID+"/"+env] {
			if h.Deployment == d.ID {
				historical = true
			}
		}
		if !historical {
			return nil, 0, problem(409, "ROLLBACK_NOT_PUBLISHED", "Rollback target must be a successfully published deployment")
		}
		deployment = d
	}
	id = randomID("op")
	op := WorkerOperation{ID: id, Kind: kind, Organization: t.Organization, Service: t.ID, Environment: env, State: "queued", Build: in.Build, Deployment: deployment.ID, ExpectedRevision: current.Revision, ConfigRevision: current.ConfigRevision, Steps: []WorkerStep{{"artifact", "pending", ""}, {"candidate", "pending", ""}, {"commit", "pending", ""}, {"synchronize", "pending", ""}}, Created: now()}
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
func (s *Server) workerDeploymentGet(st *State, p Principal, r *http.Request) (any, int, error) {
	d, ok := st.Workers.Deployments[r.PathValue("deployment")]
	if !ok || d.Service != r.PathValue("resource") || d.Organization != r.PathValue("org") {
		return nil, 0, notFound()
	}
	_, e := s.workerTarget(st, r, d.Environment, "service.version.read", p)
	return d, 200, e
}
