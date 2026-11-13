package workers

import (
	"context"

	"github.com/krun-dev/krun/internal/authorization"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/storage"
)

func (s *Service) resolveServiceBindings(ctx context.Context, p authorization.Principal, source authorization.Target, input map[string]domain.WorkerServiceBinding) (map[string]domain.WorkerServiceBinding, error) {
	if len(input) > 16 {
		return nil, domain.Invalid("WORKER_BINDINGS_INVALID", "Too many service bindings")
	}
	resolved := make(map[string]domain.WorkerServiceBinding, len(input))
	for name, binding := range input {
		if binding.Environment == "" {
			binding.Environment = "production"
		}
		worker, err := s.Store.ServiceBindingWorker(ctx, source.Organization, binding.Worker, binding.Service)
		if err != nil {
			return nil, err
		}
		if worker.ID == source.ID {
			return nil, domain.Invalid("WORKER_BINDINGS_INVALID", "A Worker cannot bind to itself")
		}
		binding.Worker, binding.Service = worker.ID, worker.Locator
		actor := WorkerActor(p)
		binding.GrantedBy = &actor
		if err = s.authorizeServiceBinding(ctx, binding); err != nil {
			return nil, err
		}
		if binding.Version != "" {
			if _, err = s.Store.ServiceBindingTarget(ctx, source.Organization, binding); err != nil {
				return nil, err
			}
		}
		resolved[name] = binding
	}
	return resolved, nil
}

func (s *Service) authorizeServiceBinding(ctx context.Context, binding domain.WorkerServiceBinding) error {
	if binding.GrantedBy == nil {
		return domain.Forbidden()
	}
	actor := *binding.GrantedBy
	ctx, err := s.Store.ActorScope(ctx, actor, binding.Worker)
	if err != nil {
		return err
	}
	return s.Store.View(ctx, storage.PartitionIAM, func(st *domain.State) error {
		return s.WorkerAuthorizeActor(st, actor, binding.Worker, binding.Environment, "worker.invoke")
	})
}

func (s *Service) ResolveServiceInvocation(ctx context.Context, caller, name, pool string) (storage.WorkerServiceTarget, error) {
	version, err := s.Store.ServiceBindingCaller(ctx, caller, pool)
	if err != nil {
		return storage.WorkerServiceTarget{}, err
	}
	binding, ok := version.ServiceBindings[name]
	if !ok {
		return storage.WorkerServiceTarget{}, domain.Forbidden()
	}
	if err = s.authorizeServiceBinding(ctx, binding); err != nil {
		return storage.WorkerServiceTarget{}, err
	}
	return s.Store.ServiceBindingTarget(ctx, version.Organization, binding)
}
