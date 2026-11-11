package workers

import (
	"github.com/krun-dev/krun/internal/authorization"
	appconfig "github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/domain"
)

func (s *Service) ResolveInvocation(st *domain.State, principal authorization.Principal, ref WorkerRef) (string, string, error) {
	env := ref.Environment
	if !appconfig.WorkerEnvValid(env) {
		return "", "", domain.NotFound()
	}
	target, err := s.WorkerTarget(st, ref, env, "worker.invoke", principal)
	if err != nil {
		return "", "", err
	}
	worker := st.Workers.Workers[target.ID]
	current := worker.Environments[env]
	if !current.Enabled || current.Version == "" {
		return "", "", domain.Conflict("WORKER_NOT_PUBLISHED", "Publish this enabled environment first")
	}
	// Invoke through the first selected domain of the worker. The domain's
	// Gateway decides which instance to address. Hosts are derived, not
	// stored, so they come from the catalog and the worker selection.
	slug := appconfig.WorkerSlug(worker.Organization, worker.Worker)
	for _, id := range s.SyncWorkerDomains(worker) {
		d, ok := s.Config.Workers.Domain(id)
		if !ok {
			continue
		}
		if candidate := appconfig.HostFor(slug, env, d.Domain); candidate != "" {
			return candidate, d.Gateway, nil
		}
	}
	return "", "", domain.Conflict("WORKER_NOT_PUBLISHED", "Environment has no routable domain")
}
