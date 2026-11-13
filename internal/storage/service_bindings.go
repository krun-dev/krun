package storage

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/krun-dev/krun/internal/domain"
)

type WorkerServiceTarget struct {
	Worker      string
	Environment string
	Pool        string
	Host        string
	Version     string
}

func (s *Store) ServiceBindingWorker(ctx context.Context, organization, worker, locator string) (domain.Resource, error) {
	var result domain.Resource
	err := s.pool.QueryRow(ctx, `SELECT resource_id,locator FROM krun_resources
		WHERE org_id=$1 AND kind='worker' AND (($2<>'' AND resource_id=$2) OR ($2='' AND locator=$3))`,
		organization, worker, locator).Scan(&result.ID, &result.Locator)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, domain.NotFound()
	}
	return result, err
}

func (s *Store) ServiceBindingCaller(ctx context.Context, id, pool string) (domain.WorkerVersion, error) {
	var version domain.WorkerVersion
	err := s.pool.QueryRow(ctx, `SELECT v.org_id,v.service_bindings
		FROM krun_worker_versions v
		JOIN krun_worker_placements p ON p.worker_id=v.worker_id AND p.org_id=v.org_id
		JOIN krun_resources r ON r.resource_id=v.worker_id AND r.org_id=v.org_id AND r.kind='worker'
		JOIN krun_worker_environments e ON e.worker_id=v.worker_id AND e.env_name=v.env_name
		WHERE v.version_id=$1 AND p.pool_id=$2 AND p.role='primary' AND p.state='active' AND e.enabled
		AND (v.version_id IN (e.version_id,e.candidate_version_id) OR EXISTS (
			SELECT 1 FROM krun_worker_versions src
			JOIN krun_worker_environments active ON active.worker_id=src.worker_id AND active.env_name=src.env_name
			CROSS JOIN LATERAL jsonb_each(src.service_bindings) b
			WHERE active.enabled AND src.org_id=v.org_id AND src.version_id IN (active.version_id,active.candidate_version_id)
			  AND b.value->>'version_id'=v.version_id
		) OR EXISTS (
			SELECT 1 FROM krun_worker_deployments d WHERE d.worker_id=v.worker_id AND d.env_name=v.env_name
			AND v.version_id IN (d.version_id,d.candidate_version_id) AND d.committed_at IS NOT NULL AND d.expires_at>now()
		))`, id, pool).Scan(&version.Organization, &version.ServiceBindings)
	if errors.Is(err, pgx.ErrNoRows) {
		return version, domain.Forbidden()
	}
	return version, err
}

func (s *Store) ServiceBindingTarget(ctx context.Context, organization string, binding domain.WorkerServiceBinding) (WorkerServiceTarget, error) {
	result := WorkerServiceTarget{Worker: binding.Worker, Environment: binding.Environment, Version: binding.Version}
	err := s.pool.QueryRow(ctx, `SELECT p.pool_id,r.host
		FROM krun_worker_environments e
		JOIN krun_resources w ON w.resource_id=e.worker_id AND w.org_id=$1 AND w.kind='worker'
		JOIN krun_worker_placements p ON p.worker_id=e.worker_id AND p.org_id=w.org_id AND p.role='primary' AND p.state='active'
		JOIN worker_config_routes r ON r.worker_id=e.worker_id AND r.env_name=e.env_name
			AND r.pool=p.pool_id AND r.enabled AND r.expires_at IS NULL
		WHERE e.worker_id=$2 AND e.env_name=$3 AND e.enabled AND e.version_id<>''
		AND ($4='' OR EXISTS(SELECT 1 FROM krun_worker_versions v WHERE v.version_id=$4
			AND v.worker_id=e.worker_id AND v.org_id=w.org_id AND v.env_name=e.env_name))
		ORDER BY r.host LIMIT 1`, organization, binding.Worker, binding.Environment, binding.Version).Scan(&result.Pool, &result.Host)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, domain.NotFound()
	}
	return result, err
}
