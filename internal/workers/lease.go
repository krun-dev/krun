package workers

import (
	"context"
	"errors"
	"time"
)

// leaseGuard renews a build claim while work runs and cancels the work when the
// claim is lost. One implementation keeps the Kubernetes and local builders on
// the same lease semantics.
type leaseGuard struct {
	cancel context.CancelFunc
	done   chan struct{}
	lost   chan error
}

func (w *Controller) guardBuildLease(parent context.Context, id string) (context.Context, *leaseGuard) {
	ctx, cancel := context.WithCancel(parent)
	guard := &leaseGuard{cancel: cancel, done: make(chan struct{}), lost: make(chan error, 1)}
	go func() {
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-guard.done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				owned, err := w.Service.Store.RenewBuildClaim(ctx, id, w.Replica)
				if err == nil && owned {
					continue
				}
				if err == nil {
					err = errors.New("build lease lost")
				}
				select {
				case <-guard.done:
					return
				default:
					guard.lost <- err
					cancel()
					return
				}
			}
		}
	}()
	return ctx, guard
}

// lostErr consumes a lease failure if one was reported. Callers use it to skip
// finalization after another replica took over the build.
func (g *leaseGuard) lostErr() error {
	select {
	case err := <-g.lost:
		return err
	default:
		return nil
	}
}

func (g *leaseGuard) stop() error {
	close(g.done)
	g.cancel()
	return g.lostErr()
}
