package workers

import (
	"context"
	"errors"
	"fmt"
)

// CollectDeletedWorkerResources retries each durable task until the Kubernetes
// lists are empty. Shared routes and serving resources are managed elsewhere.
func (w *Controller) CollectDeletedWorkerResources(ctx context.Context) error {
	tasks, err := w.Service.Store.PendingWorkerCleanups(ctx, 32)
	if err != nil {
		return err
	}
	var failures []error
	for _, task := range tasks {
		count := 0
		after := ""
		var cleanupErr error
		for {
			jobs, e := w.Service.Store.WorkerCleanupLegacyJobs(ctx, task.Worker, after, 100)
			if e != nil {
				cleanupErr = e
				break
			}
			n, e := w.Kube.CleanupBuildResources(ctx, task.Organization, task.Worker, jobs)
			count += n
			if e != nil {
				cleanupErr = e
				break
			}
			if len(jobs) < 100 {
				break
			}
			after = jobs[len(jobs)-1]
		}
		message := ""
		if cleanupErr != nil {
			message = cleanupErr.Error()
			failures = append(failures, fmt.Errorf("worker %s cleanup: %w", task.Worker, cleanupErr))
		}
		if e := w.Service.Store.UpdateWorkerCleanup(ctx, task.Worker, cleanupErr == nil && count == 0, message); e != nil {
			failures = append(failures, e)
		}
	}
	return errors.Join(failures...)
}
