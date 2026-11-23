package workers

import (
	"context"
	"time"

	"github.com/krun-dev/krun/internal/authorization"
	"github.com/krun-dev/krun/internal/domain"
)

func (s *Service) WorkerPropagation(ctx context.Context, st *domain.State, p authorization.Principal, ref WorkerRef, environment string) (any, Outcome, error) {
	target, err := s.WorkerTarget(st, ref, environment, "worker.read", p)
	if err != nil {
		return nil, Read, err
	}
	if s.Config.Workers.RuntimeConfig.Protocol != 2 {
		return nil, Read, domain.Invalid("CONFIG_PROTOCOL_REQUIRED", "Resource configuration protocol is not enabled")
	}
	placement, ok := domain.ActiveWorkerPlacement(st.Workers, target.ID)
	if !ok {
		return nil, Read, domain.Unavailable("PLACEMENT_UNAVAILABLE", "Runtime placement unavailable")
	}
	pool, ok := s.Config.Workers.RuntimePool(placement.Pool)
	if !ok {
		return nil, Read, domain.NotFound()
	}
	pods := []string{}
	confirmed := false
	if s.Controller != nil && s.Controller.Kube != nil {
		bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
		members, err := s.Controller.Kube.RuntimeMembers(bounded, pool.ID, pool.RuntimeService)
		cancel()
		if err == nil {
			confirmed = true
			for _, pod := range members {
				pods = append(pods, pod.UID)
			}
		}
	}
	out, err := s.Store.RuntimePropagation(ctx, pool.ID, target.ID, environment, pods, confirmed)
	return out, Read, err
}
