package storage

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/krun-dev/krun/internal/domain"
)

// loadWorkerActivity reads only environment names for authorization and the
// worker's unfinished builds and operations. Terminal history and release
// metadata belong to the full overview.
func (s *Store) loadWorkerActivity(ctx context.Context, st *domain.State, org, worker string) error {
	batch := &pgx.Batch{}
	batch.Queue(`SELECT env_name FROM krun_worker_environments WHERE worker_id=$1 AND env_name<>$2`, worker, domain.PreviewBaseName)
	batch.Queue(`SELECT `+workerBuildColumns+` FROM krun_worker_builds
		WHERE org_id=$1 AND worker_id=$2 AND state NOT IN ('succeeded','failed')`, org, worker)
	batch.Queue(`SELECT `+workerOperationColumns+` FROM krun_worker_operations
		WHERE org_id=$1 AND worker_id=$2 AND state NOT IN ('succeeded','failed','available','superseded')`, org, worker)
	results := s.pool.SendBatch(ctx, batch)
	defer results.Close()

	rows, err := results.Query()
	if err != nil {
		return err
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	envs := make(map[string]domain.WorkerEnvironment, len(names))
	for _, name := range names {
		envs[name] = domain.WorkerEnvironment{Name: name}
	}
	st.Workers.Workers[worker] = domain.Worker{Worker: worker, Organization: org, Environments: envs}

	if err := readWorkerBuilds(results, st); err != nil {
		return err
	}
	return readWorkerOperations(results, st)
}
