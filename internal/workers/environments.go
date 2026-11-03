package workers

import (
	context "context"
	"sort"

	authorization "github.com/krun-dev/krun/internal/authorization"
	appconfig "github.com/krun-dev/krun/internal/config"
	domain "github.com/krun-dev/krun/internal/domain"
)

type WorkerEnvironmentCreateInput struct {
	Name       string `json:"name"`
	IgnoreBase bool   `json:"ignore_base_config,omitempty"`
}

type WorkerEnvironmentUpdateInput struct {
	Revision  int               `json:"expected_revision"`
	Enabled   *bool             `json:"enabled"`
	Variables map[string]string `json:"variables"`
}

func (s *Service) WorkerEnvironments(ctx context.Context, st *domain.State, p authorization.Principal, ref ServiceRef) (any, Outcome, error) {
	t, e := s.WorkerCollectionTarget(st, ref, "service.read", p)
	if e != nil {
		return nil, Read, e
	}
	ws := s.WorkerServiceView(st, t)
	names := make([]string, 0, len(ws.Environments))
	for name := range ws.Environments {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		if names[i] == appconfig.WorkerBaseline {
			return true
		}
		if names[j] == appconfig.WorkerBaseline {
			return false
		}
		return names[i] < names[j]
	})
	out := []domain.WorkerEnvironment{}
	for _, name := range names {
		d, e := authorization.Authorize(st, p, "service.read", authorization.Target{Kind: "service", ID: t.ID, Organization: t.Organization, Environment: name})
		if e != nil {
			return nil, Read, e
		}
		if d.Allowed {
			env := ws.Environments[name]
			// Recompute rather than trusting persisted hosts: the configured
			// domain catalog may have changed since the row was written.
			s.SyncEnvironmentHosts(&env, t.Organization, t.ID, ws.Domains, ws.NetworkProfile, s.Config.Workers.ServicePool(st.Workers, ws))
			out = append(out, env)
		}
	}
	return collection(out)

}

func (s *Service) WorkerEnvironmentCreate(ctx context.Context, st *domain.State, p authorization.Principal, ref ServiceRef, in WorkerEnvironmentCreateInput) (any, Outcome, error) {
	if !appconfig.WorkerPreviewValid(in.Name) {
		return nil, Read, domain.Problem(422, "ENVIRONMENT_INVALID", "Preview name must be a slug and cannot be production")
	}
	t, e := s.WorkerTarget(st, ref, in.Name, "service.update", p)
	if e != nil {
		return nil, Read, e
	}
	ws := s.WorkerService(st, t)
	if _, exists := ws.Environments[in.Name]; exists {
		return nil, Read, domain.Problem(409, "ENVIRONMENT_EXISTS", "Preview environment already exists")
	}
	previews := 0
	for name := range ws.Environments {
		if name != appconfig.WorkerBaseline {
			previews++
		}
	}
	limit := s.Config.Workers.PreviewLimit
	if limit <= 0 {
		limit = 32
	}
	if previews >= limit {
		return nil, Read, domain.Problem(409, "ENVIRONMENT_LIMIT", "Too many preview environments")
	}
	env := domain.WorkerEnvironment{Name: in.Name, Enabled: true, ConfigRevision: 1, Variables: map[string]string{}, SecretVersions: map[string]string{}}
	if ws.PreviewBase != nil && !in.IgnoreBase {
		env.Variables = domain.CloneStringsMap(ws.PreviewBase.Variables)
		env.SecretVersions = domain.CloneStringsMap(ws.PreviewBase.SecretVersions)
		env.SecretNames = domain.WorkerSecretNames(env.SecretVersions)
	}
	s.SyncEnvironmentHosts(&env, t.Organization, t.ID, ws.Domains, ws.NetworkProfile, s.Config.Workers.ServicePool(st.Workers, ws))
	ws.Environments[in.Name] = env
	ws.PreviewRevision++
	st.Workers.Services[t.ID] = ws
	s.audit(st, p, "workers.environment.create", t.ID+"/"+in.Name, true)
	return env, Created, nil

}

func (s *Service) WorkerEnvironmentDelete(ctx context.Context, st *domain.State, p authorization.Principal, ref ServiceRef) (any, Outcome, error) {
	env := ref.Environment
	if env == appconfig.WorkerBaseline || !appconfig.WorkerEnvValid(env) {
		return nil, Read, domain.NotFound()
	}
	t, e := s.WorkerTarget(st, ref, env, "service.update", p)
	if e != nil {
		return nil, Read, e
	}
	ws := s.WorkerService(st, t)
	if _, exists := ws.Environments[env]; !exists {
		return nil, Read, domain.NotFound()
	}
	if WorkerBusy(st, t.ID, env) {
		return nil, Read, domain.Problem(409, "DEPLOYMENT_BUSY", "Wait for the active version operation")
	}
	delete(ws.Environments, env)
	ws.PreviewRevision++
	st.Workers.Services[t.ID] = ws
	s.audit(st, p, "workers.environment.delete", t.ID+"/"+env, true)
	return nil, Deleted, nil

}

func (s *Service) WorkerEnvironmentUpdate(ctx context.Context, st *domain.State, p authorization.Principal, ref ServiceRef, t authorization.Target, env string, in WorkerEnvironmentUpdateInput) (any, Outcome, error) {
	ws := s.WorkerService(st, t)
	current, exists := ws.Environments[env]
	if !exists {
		return nil, Read, domain.NotFound()
	}
	if current.ConfigRevision != in.Revision {
		return nil, Read, domain.Problem(409, "REVISION_CONFLICT", "Environment configuration changed")
	}
	if WorkerBusy(st, t.ID, env) {
		return nil, Read, domain.Problem(409, "DEPLOYMENT_BUSY", "Wait for the active version operation")
	}
	if in.Variables != nil && !appconfig.WorkerVariablesValid(in.Variables) {
		return nil, Read, domain.Problem(422, "WORKER_VARIABLES_INVALID", "Use up to 32 non-reserved uppercase variables, at most 16 KiB")
	}
	if in.Variables != nil {
		for name := range in.Variables {
			if _, exists := current.SecretVersions[name]; exists {
				return nil, Read, domain.Problem(409, "WORKER_BINDING_CONFLICT", "A Secret already uses this binding name")
			}
		}
	}
	if in.Enabled != nil {
		if env == appconfig.WorkerBaseline && !*in.Enabled {
			return nil, Read, domain.Problem(422, "ENVIRONMENT_INVALID", "The baseline environment cannot be disabled")
		}
		current.Enabled = *in.Enabled
	}
	if in.Variables != nil {
		current.Variables = in.Variables
	}
	s.SyncEnvironmentHosts(&current, t.Organization, t.ID, ws.Domains, ws.NetworkProfile, s.Config.Workers.ServicePool(st.Workers, ws))
	current.ConfigRevision++
	ws.Environments[env] = current
	st.Workers.Services[t.ID] = ws
	return current, Read, nil

}

type WorkerEnvironmentSummary struct {
	Name      string   `json:"name"`
	Domains   []string `json:"domains,omitempty"`
	Hosts     []string `json:"hosts,omitempty"`
	Enabled   bool     `json:"enabled"`
	Version   string   `json:"version_id,omitempty"`
	Confirmed string   `json:"confirmed_version_id,omitempty"`
}
