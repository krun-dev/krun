package workers

import (
	"context"
	"errors"

	"github.com/krun-dev/krun/internal/adapters/runtimeclient"
	appconfig "github.com/krun-dev/krun/internal/config"
)

// VerifyRuntimeSession binds a stream to the Pod reached through Kubernetes,
// rather than trusting a PodUID asserted by a holder of the shared pool token.
func (w *Controller) VerifyRuntimeSession(ctx context.Context, pool appconfig.WorkerRuntimePool, uid, proof string) error {
	if uid == "" || len(uid) > 128 || len(proof) != 64 || w.Kube == nil || w.Http == nil {
		return errors.New("invalid runtime session identity")
	}
	pod, err := w.Kube.RuntimePod(ctx, pool.ID, uid)
	if err != nil {
		return err
	}
	identity, status, err := runtimeclient.SessionIdentity(ctx, w.Http, pool, pod.IP, proof)
	if err != nil {
		return err
	}
	if status != 200 || identity["pod_uid"] != uid || identity["pool"] != pool.ID {
		return errors.New("runtime session identity mismatch")
	}
	return nil
}
