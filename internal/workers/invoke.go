package workers

import (
	"github.com/krun-dev/krun/internal/authorization"
	appconfig "github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/domain"
)

func (s *Service) ResolveInvocation(st *domain.State, principal authorization.Principal, ref ServiceRef) (string, string, error) {
	env := ref.Environment
	if !appconfig.WorkerEnvValid(env) {
		return "", "", domain.NotFound()
	}
	target, err := s.WorkerTarget(st, ref, env, "service.invoke", principal)
	if err != nil {
		return "", "", err
	}
	service := st.Workers.Services[target.ID]
	current := service.Environments[env]
	if !current.Enabled || current.Version == "" {
		return "", "", domain.Problem(409, "WORKER_NOT_PUBLISHED", "Publish this enabled environment first")
	}
	// Invoke through the first selected domain of the service. The domain's
	// Gateway decides which instance to address. Hosts are derived, not
	// stored, so they come from the catalog and the service selection.
	slug := appconfig.WorkerSlug(service.Organization, service.Service)
	for _, id := range s.SyncServiceDomains(service) {
		d, ok := s.Config.Workers.Domain(id)
		if !ok {
			continue
		}
		suffix := d.Domain
		if pool := s.Config.Workers.ServicePool(st.Workers, service); pool != "" {
			suffix = s.Config.Workers.PoolHostSuffix(pool, d.Domain)
		}
		if candidate := appconfig.HostFor(slug, env, suffix); candidate != "" {
			return candidate, d.Gateway, nil
		}
	}
	return "", "", domain.Problem(409, "WORKER_NOT_PUBLISHED", "Environment has no routable domain")
}
