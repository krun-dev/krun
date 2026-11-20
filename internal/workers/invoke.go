package workers

import (
	"context"
	"github.com/krun-dev/krun/internal/authorization"
	appconfig "github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/domain"
)

func (s *Service) ResolveInvocation(ctx context.Context, st *domain.State, principal authorization.Principal, ref WorkerRef) (string, string, error) {
	env := ref.Environment
	if !appconfig.WorkerEnvValid(env) {
		return "", "", domain.NotFound()
	}
	target, err := s.WorkerTarget(st, ref, env, "worker.invoke", principal)
	if err != nil {
		return "", "", err
	}
	worker := s.WorkerView(st, target)
	current := worker.Environments[env]
	if env == appconfig.WorkerBaseline && worker.WorkersDevDisabled {
		return "", "", domain.Conflict("WORKER_NOT_PUBLISHED", "The default production URL is disabled")
	}
	if !current.Enabled || current.Version == "" {
		return "", "", domain.Conflict("WORKER_NOT_PUBLISHED", "Publish this enabled environment first")
	}
	if env != appconfig.WorkerBaseline && worker.PreviewURLsDisabled {
		domains, err := s.CustomPreviewDomains(ctx, s.NormalizeWorker(worker))
		if err != nil {
			return "", "", err
		}
		for _, domain := range domains {
			if host := customPreviewHost(domain, env, ""); host != "" {
				if suffix, ok := s.Config.Workers.DomainForHost(domain); ok {
					return host, suffix.Gateway, nil
				}
			}
		}
		return "", "", domain.Conflict("WORKER_NOT_PUBLISHED", "Environment has no routable domain")
	}
	// Invoke through the first selected domain of the worker. The domain's
	// Gateway decides which instance to address. Hosts are derived, not
	// stored, so they come from the catalog and the worker selection.
	slug := worker.AddressName
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
