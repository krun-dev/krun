package storage

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/krun-dev/krun/internal/domain"
)

// RuntimeBucket resolves exactly one live, currently deployed binding. No hot
// path writes or tenant-wide IAM/state load are needed.
func (s *Store) RuntimeBucket(ctx context.Context, versionID, binding, pool string) (domain.WorkerBucket, error) {
	var bucket domain.WorkerBucket
	err := s.pool.QueryRow(ctx, `SELECT k.bucket_id,k.org_id,k.name,k.store_id,k.enabled,k.revision,k.created_at
 FROM krun_worker_version_r2 b JOIN krun_worker_versions v ON v.version_id=b.version_id
 JOIN krun_worker_buckets k ON k.bucket_id=b.bucket_id AND k.org_id=v.org_id AND k.enabled
 JOIN krun_worker_environments e ON e.worker_id=v.worker_id AND e.env_name=v.env_name AND e.enabled
 JOIN krun_workers w ON w.worker_id=v.worker_id AND w.org_id=v.org_id
 JOIN krun_resources r ON r.resource_id=v.worker_id AND r.org_id=v.org_id AND r.kind='worker'
 JOIN krun_organizations o ON o.org_id=v.org_id
 WHERE b.version_id=$1 AND b.name=$2 AND EXISTS(SELECT 1 FROM krun_worker_placements p WHERE p.worker_id=v.worker_id AND p.org_id=v.org_id AND p.pool_id=$3 AND p.state IN ('preparing','ready','active'))
 AND (e.version_id=v.version_id OR EXISTS(SELECT 1 FROM krun_worker_deployments d WHERE d.version_id=v.version_id AND d.committed_at IS NOT NULL AND d.expires_at>now()))`, versionID, binding, pool).Scan(&bucket.ID, &bucket.Organization, &bucket.Name, &bucket.Store, &bucket.Enabled, &bucket.Revision, &bucket.Created)
	if errors.Is(err, pgx.ErrNoRows) {
		err = domain.NotFound()
	}
	return bucket, err
}
