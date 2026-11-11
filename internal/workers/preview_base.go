package workers

import (
	"context"

	"github.com/krun-dev/krun/internal/authorization"
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
	if err := validateWorkerEnvironmentVariables(in.Variables, base.SecretVersions, "Invalid variables"); err != nil {
		return nil, Read, err
	}
	if in.Variables != nil {
		base.Variables = domain.CloneWorkerVariables(in.Variables)
	}
	if err := s.applyWorkerEnvironmentBindings(ctx, st, ref.Organization, &base, *in); err != nil {
		return nil, Read, err
	}
	base.ConfigRevision++
	worker.PreviewBase = &base
	st.Workers.Workers[ref.Worker] = worker
	return base, Read, nil
}
