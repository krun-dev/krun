package workers

import (
	"context"
	"math/rand/v2"

	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/storage"
)

func (w *Controller) observeResourceOperation(ctx context.Context, op domain.WorkerOperation, state domain.WorkerState, routesReady bool) error {
	note := func(message string) error {
		return w.Service.Store.NoteResourceOperation(ctx, op.ID, message)
	}
	if !routesReady {
		return note("Committed; waiting for Gateway and HTTPRoute confirmation")
	}
	placement, ok := domain.ActiveWorkerPlacement(&state, op.Worker)
	if !ok {
		return note("Committed; waiting for runtime placement")
	}
	pool, ok := w.Config.RuntimePool(placement.Pool)
	if !ok {
		return note("Committed; waiting for runtime pool")
	}
	if _, err := w.PublishConfig(context.WithValue(ctx, storage.WorkerConfigPriorityKey{}, op.Worker)); err != nil {
		return err
	}
	target, err := w.Service.Store.PinOperationResourceGeneration(ctx, op.ID, pool.ID)
	if err != nil {
		return err
	}
	if target.Generation == 0 {
		return note("Committed; waiting for runtime route bindings")
	}
	pods, err := w.Kube.RuntimeMembers(ctx, pool.ID, pool.RuntimeService)
	if err != nil {
		return err
	}
	if len(pods) == 0 {
		return note("Committed; waiting for serving runtime members")
	}
	versions := []string{op.Version}
	if op.Candidate != "" && op.CandidateWeight > 0 {
		if op.CandidateWeight == 100000 {
			versions = []string{op.Candidate}
		} else {
			versions = append(versions, op.Candidate)
		}
	}
	const maxProbePods = 2
	start := 0
	if len(pods) > maxProbePods {
		start = rand.IntN(len(pods))
	}
	for i := range min(len(pods), maxProbePods) {
		pod := pods[(start+i)%len(pods)]
		prepared := true
		for _, version := range versions {
			probe, status, err := w.Management(ctx, pool, pod, "", "/config-probe", map[string]any{
				"host": target.Host, "path": target.Path, "worker_id": op.Worker, "environment": op.Environment,
				"version_id": version, "generation": target.Generation,
			})
			if err != nil || status != 200 || probe["pod_uid"] != pod.UID || probe["pool"] != pool.ID || probe["config_epoch"] != target.Epoch || probe["version_id"] != version || probe["config_protocol"] != float64(2) {
				prepared = false
				break
			}
			generation, ok := probe["target_generation"].(float64)
			if !ok || generation < float64(target.Generation) {
				prepared = false
				break
			}
		}
		if !prepared {
			continue
		}
		cursor := domain.EdgeConfigCursor{Epoch: target.Epoch, Revision: target.Generation}
		if op.Kind == domain.WorkerVersionURLOperation {
			return w.Service.Store.ConfirmResourceVersionURL(ctx, op.ID, pool.ID, cursor)
		}
		return w.Service.Store.ConfirmOperation(ctx, op.ID, pool.ID, cursor, true)
	}

	return w.Service.Store.NoteResourceOperation(ctx, op.ID, "Committed; waiting for serving configuration and version preparation probe")
}
