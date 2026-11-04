package workers

import (
	"context"
	"github.com/krun-dev/krun/internal/authorization"
	appconfig "github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/domain"
)

func (s *Service) PreviewBase(ctx context.Context, st *domain.State, p authorization.Principal, ref ServiceRef, in *WorkerEnvironmentUpdateInput) (any, Outcome, error) {
	action := "service.read"
	if in != nil {
		action = "service.update"
	}
	target, err := s.WorkerTarget(st, ref, "global", action, p)
	if err != nil {
		return nil, Read, err
	}
	service := s.WorkerServiceView(st, target)
	base := domain.WorkerEnvironment{Name: domain.PreviewBaseName, Enabled: true, Variables: map[string]any{}, SecretVersions: map[string]string{}}
	if service.PreviewBase != nil {
		base = *service.PreviewBase
	}
	if in == nil {
		return base, Read, nil
	}
	if in.Revision != base.ConfigRevision {
		return nil, Read, domain.Problem(409, "REVISION_CONFLICT", "Previews Base configuration changed")
	}
	if in.Variables != nil {
		if !appconfig.WorkerVariablesValid(in.Variables) {
			return nil, Read, domain.Problem(422, "WORKER_VARIABLES_INVALID", "Invalid variables")
		}
		for name := range in.Variables {
			if _, exists := base.SecretVersions[name]; exists {
				return nil, Read, domain.Problem(409, "WORKER_BINDING_CONFLICT", "A Secret already uses this binding name")
			}
		}
		base.Variables = domain.CloneWorkerVariables(in.Variables)
	}
	if in.VersionMetadata != nil {
		base.VersionMetadata = *in.VersionMetadata
	}
	if in.R2Buckets != nil {
		if s.Config.Workers == nil || s.Config.Workers.ObjectStorage == nil || s.Config.Workers.ObjectStorage.CursorKey == "" {
			return nil, Read, domain.Problem(503, "OBJECT_STORAGE_NOT_CONFIGURED", "R2 storage must be configured")
		}
		resolved, err := resolveR2Bindings(st, ref.Organization, *in.R2Buckets)
		if err != nil {
			return nil, Read, err
		}
		base.R2Bindings = resolved
	}
	if err := ValidateWorkerBindings(base); err != nil {
		return nil, Read, err
	}
	base.ConfigRevision++
	service.PreviewBase = &base
	st.Workers.Services[ref.Service] = service
	return base, Read, nil
}
