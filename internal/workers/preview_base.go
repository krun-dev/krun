package workers

import (
	"context"
	"github.com/krun-dev/krun/internal/authorization"
	appconfig "github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/domain"
)

func (s *Service) PreviewBase(ctx context.Context, st *domain.State, p authorization.Principal, ref WorkerRef, in *WorkerEnvironmentUpdateInput) (any, Outcome, error) {
	action := "worker.read"
	if in != nil {
		action = "worker.update"
	}
	target, err := s.WorkerTarget(st, ref, "global", action, p)
	if err != nil {
		return nil, Read, err
	}
	worker := s.WorkerView(st, target)
	base := domain.WorkerEnvironment{Name: domain.PreviewBaseName, Enabled: true, Variables: map[string]any{}, SecretVersions: map[string]string{}}
	if worker.PreviewBase != nil {
		base = *worker.PreviewBase
	}
	if in == nil {
		return base, Read, nil
	}
	if in.Revision != base.ConfigRevision {
		return nil, Read, domain.Conflict("REVISION_CONFLICT", "Previews Base configuration changed")
	}
	if in.Variables != nil {
		if !appconfig.WorkerVariablesValid(in.Variables) {
			return nil, Read, domain.Invalid("WORKER_VARIABLES_INVALID", "Invalid variables")
		}
		for name := range in.Variables {
			if _, exists := base.SecretVersions[name]; exists {
				return nil, Read, domain.Conflict("WORKER_BINDING_CONFLICT", "A Secret already uses this binding name")
			}
		}
		base.Variables = domain.CloneWorkerVariables(in.Variables)
	}
	if in.VersionMetadata != nil {
		base.VersionMetadata = *in.VersionMetadata
	}
	if in.R2Buckets != nil {
		if s.Config.Workers == nil || s.Config.Workers.ObjectStorage == nil || s.Config.Workers.ObjectStorage.CursorKey == "" {
			return nil, Read, domain.Unavailable("OBJECT_STORAGE_NOT_CONFIGURED", "R2 storage must be configured")
		}
		resolved, err := resolveR2Bindings(st, ref.Organization, *in.R2Buckets)
		if err != nil {
			return nil, Read, err
		}
		base.R2Bindings = resolved
	}
	if in.HyperdriveBindings != nil {
		if err := s.validateHyperdriveBindings(ctx, ref.Organization, *in.HyperdriveBindings); err != nil {
			return nil, Read, err
		}
		base.HyperdriveBindings = cloneHyperdriveBindings(*in.HyperdriveBindings)
	}
	if err := ValidateWorkerBindings(base); err != nil {
		return nil, Read, err
	}
	base.ConfigRevision++
	worker.PreviewBase = &base
	st.Workers.Workers[ref.Worker] = worker
	return base, Read, nil
}
