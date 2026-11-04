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
	if s.pool == nil {
		var bucket domain.WorkerBucket
		err := s.View(ctx, PartitionAll, func(st *domain.State) error {
			version, exists := st.Workers.Versions[versionID]
			if !exists || !domain.WorkerServiceAvailableInPool(st.Workers, version.Service, pool) || st.Resources[version.Service].ID == "" || st.Organizations[version.Organization].ID == "" {
				return domain.NotFound()
			}
			service := st.Workers.Services[version.Service]
			env := service.Environments[version.Environment]
			deployed := env.Version == versionID
			for _, deployment := range st.Workers.Deployments {
				if deployment.Version == versionID && deployment.Committed != nil && deployment.Expires.After(now()) {
					deployed = true
				}
			}
			if !env.Enabled || !deployed {
				return domain.NotFound()
			}
			ref, exists := version.R2Bindings[binding]
			if !exists {
				return domain.NotFound()
			}
			bucket, exists = st.Workers.Buckets[ref.BucketID]
			if !exists || !bucket.Enabled || bucket.Organization != version.Organization {
				return domain.NotFound()
			}
			return nil
		})
		return bucket, err
	}
	var bucket domain.WorkerBucket
	err := s.pool.QueryRow(ctx, `SELECT k.bucket_id,k.org_id,k.name,k.store_id,k.enabled,k.revision,k.created_at
 FROM krun_worker_version_r2 b JOIN krun_worker_versions v ON v.version_id=b.version_id
 JOIN krun_worker_buckets k ON k.bucket_id=b.bucket_id AND k.org_id=v.org_id AND k.enabled
 JOIN krun_worker_environments e ON e.service_id=v.service_id AND e.env_name=v.env_name AND e.enabled
 JOIN krun_worker_services w ON w.service_id=v.service_id AND w.org_id=v.org_id
 JOIN krun_resources r ON r.resource_id=v.service_id AND r.org_id=v.org_id AND r.kind='service'
 JOIN krun_organizations o ON o.org_id=v.org_id
 WHERE b.version_id=$1 AND b.name=$2 AND EXISTS(SELECT 1 FROM krun_worker_service_placements p WHERE p.service_id=v.service_id AND p.org_id=v.org_id AND p.pool_id=$3 AND p.state IN ('preparing','ready','active'))
 AND (e.version_id=v.version_id OR EXISTS(SELECT 1 FROM krun_worker_deployments d WHERE d.version_id=v.version_id AND d.committed_at IS NOT NULL AND d.expires_at>now()))`, versionID, binding, pool).Scan(&bucket.ID, &bucket.Organization, &bucket.Name, &bucket.Store, &bucket.Enabled, &bucket.Revision, &bucket.Created)
	if errors.Is(err, pgx.ErrNoRows) {
		err = domain.NotFound()
	}
	return bucket, err
}
