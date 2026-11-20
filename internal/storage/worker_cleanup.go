package storage

import (
	"context"
)

type WorkerCleanup struct {
	Worker       string
	Organization string
}

// PendingWorkerCleanups returns only a bounded page. Cleanup is idempotent, so
// replicas can safely process the same task without a process-wide leader.
func (s *Store) PendingWorkerCleanups(ctx context.Context, limit int) ([]WorkerCleanup, error) {
	rows, err := s.pool.Query(ctx, `SELECT worker_id,org_id FROM krun_worker_cleanup WHERE next_attempt_at<=now() ORDER BY next_attempt_at,worker_id LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []WorkerCleanup
	for rows.Next() {
		var item WorkerCleanup
		if err := rows.Scan(&item.Worker, &item.Organization); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) WorkerCleanupLegacyJobs(ctx context.Context, worker, after string, limit int) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT job FROM krun_worker_builds WHERE worker_id=$1 AND job>$2 AND job<>'' ORDER BY job LIMIT $3`, worker, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var jobs []string
	for rows.Next() {
		var job string
		if err := rows.Scan(&job); err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

func (s *Store) UpdateWorkerCleanup(ctx context.Context, worker string, complete bool, failure string) error {
	if len(failure) > 1000 {
		failure = failure[:1000]
	}
	_, err := s.pool.Exec(ctx, `UPDATE krun_worker_cleanup SET attempts=attempts+1,last_error=$2,next_attempt_at=now()+CASE WHEN $3 THEN interval '1 hour' ELSE interval '15 seconds' END,completed_at=CASE WHEN $3 THEN COALESCE(completed_at,now()) ELSE NULL END WHERE worker_id=$1`, worker, failure, complete)
	return err
}
