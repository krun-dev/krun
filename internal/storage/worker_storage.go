package storage

import (
	"context"
	"errors"
	"reflect"

	"github.com/jackc/pgx/v5"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/storage/models"
)

// Render typed Bun mutations into the existing pgx Workers transaction. This
// keeps bucket references and their environment CAS in one bounded transaction.
type workerStorageMutation interface{ String() string }

func storageMutation(ctx context.Context, tx pgx.Tx, query workerStorageMutation, requireOne bool) error {
	tag, err := tx.Exec(ctx, query.String())
	if err != nil {
		return err
	}
	if requireOne && tag.RowsAffected() != 1 {
		return domain.Problem(409, "REVISION_CONFLICT", "Storage configuration changed")
	}
	return nil
}

func (s *Store) writeWorkerBuckets(ctx context.Context, tx pgx.Tx, before, after *domain.WorkerState) error {
	for id, bucket := range after.Buckets {
		previous, exists := before.Buckets[id]
		row := models.Bucket{
			BucketID:  id,
			OrgID:     bucket.Organization,
			Name:      bucket.Name,
			StoreID:   bucket.Store,
			Enabled:   bucket.Enabled,
			Revision:  bucket.Revision,
			CreatedAt: bucket.Created,
		}
		if !exists {
			if err := storageMutation(ctx, tx, s.orm.NewInsert().Model(&row).On("CONFLICT DO NOTHING").Returning("NULL"), true); err != nil {
				return err
			}
			continue
		}
		immutableSame := previous.ID == bucket.ID &&
			previous.Organization == bucket.Organization &&
			previous.Name == bucket.Name &&
			previous.Store == bucket.Store &&
			previous.Created.Equal(bucket.Created)
		if immutableSame && previous.Enabled == bucket.Enabled && previous.Revision == bucket.Revision {
			continue
		}
		if !immutableSame || bucket.Revision != previous.Revision+1 {
			return errors.New("invalid bucket mutation")
		}
		if err := storageMutation(ctx, tx, s.orm.NewUpdate().Model(&row).Column("enabled", "revision").WherePK().Where("revision = ?", previous.Revision), true); err != nil {
			return err
		}
	}
	for id := range before.Buckets {
		if _, exists := after.Buckets[id]; !exists {
			return errors.New("bucket deletion requires explicit data lifecycle handling")
		}
	}
	return nil
}

func (s *Store) writeWorkerR2Bindings(ctx context.Context, tx pgx.Tx, before, after *domain.WorkerState) error {
	for id, service := range after.Services {
		old := domain.StorageWorkerEnvironments(before.Services[id])
		for env, desired := range domain.StorageWorkerEnvironments(service) {
			if reflect.DeepEqual(old[env].R2Bindings, desired.R2Bindings) {
				continue
			}
			if err := storageMutation(ctx, tx, s.orm.NewDelete().Model((*models.EnvironmentR2)(nil)).Where("service_id = ? AND env_name = ?", id, env), false); err != nil {
				return err
			}
			for name, binding := range desired.R2Bindings {
				if err := checkR2Bucket(ctx, tx, binding.BucketID, service.Organization); err != nil {
					return err
				}
				row := models.EnvironmentR2{ServiceID: id, EnvName: env, Name: name, BucketID: binding.BucketID}
				if err := storageMutation(ctx, tx, s.orm.NewInsert().Model(&row).Returning("NULL"), true); err != nil {
					return err
				}
			}
		}
	}
	for id, version := range after.Versions {
		if old, exists := before.Versions[id]; exists {
			if !reflect.DeepEqual(old.R2Bindings, version.R2Bindings) {
				return errors.New("version storage bindings are immutable")
			}
			continue
		}
		for name, binding := range version.R2Bindings {
			if err := checkR2Bucket(ctx, tx, binding.BucketID, version.Organization); err != nil {
				return err
			}
			row := models.VersionR2{VersionID: id, Name: name, BucketID: binding.BucketID}
			if err := storageMutation(ctx, tx, s.orm.NewInsert().Model(&row).Returning("NULL"), true); err != nil {
				return err
			}
		}
	}
	// Explicit deletion also catches removed environments and versions. Runtime
	// grant reads require the owning rows, so an orphan can never authorize I/O.
	for id, service := range before.Services {
		current := domain.StorageWorkerEnvironments(after.Services[id])
		for env := range domain.StorageWorkerEnvironments(service) {
			if _, exists := current[env]; !exists {
				if err := storageMutation(ctx, tx, s.orm.NewDelete().Model((*models.EnvironmentR2)(nil)).Where("service_id = ? AND env_name = ?", id, env), false); err != nil {
					return err
				}
			}
		}
	}
	for id := range before.Versions {
		if _, exists := after.Versions[id]; !exists {
			if err := storageMutation(ctx, tx, s.orm.NewDelete().Model((*models.VersionR2)(nil)).Where("version_id = ?", id), false); err != nil {
				return err
			}
		}
	}
	return nil
}

func checkR2Bucket(ctx context.Context, tx pgx.Tx, bucket, organization string) error {
	var exists bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM krun_worker_buckets WHERE bucket_id=$1 AND org_id=$2 AND enabled)`, bucket, organization).Scan(&exists)
	if err != nil {
		return err
	}
	if !exists {
		return domain.Problem(409, "STORAGE_BUCKET_UNAVAILABLE", "Bucket is unavailable")
	}
	return nil
}

func readWorkerBuckets(results pgx.BatchResults, st *domain.State) error {
	rows, err := results.Query()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var bucket domain.WorkerBucket
		if err := rows.Scan(&bucket.ID, &bucket.Organization, &bucket.Name, &bucket.Store, &bucket.Enabled, &bucket.Revision, &bucket.Created); err != nil {
			return err
		}
		st.Workers.Buckets[bucket.ID] = bucket
	}
	return rows.Err()
}

func readWorkerEnvironmentR2(results pgx.BatchResults, st *domain.State) error {
	rows, err := results.Query()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var serviceID, env, name, bucketID string
		if err := rows.Scan(&serviceID, &env, &name, &bucketID); err != nil {
			return err
		}
		service, exists := st.Workers.Services[serviceID]
		if !exists {
			continue
		}
		current, exists := domain.StorageWorkerEnvironments(service)[env]
		if !exists {
			continue
		}
		if current.R2Bindings == nil {
			current.R2Bindings = map[string]domain.WorkerR2Binding{}
		}
		current.R2Bindings[name] = domain.WorkerR2Binding{StoreID: st.Workers.Buckets[bucketID].Store, BucketID: bucketID, BucketName: st.Workers.Buckets[bucketID].Name}
		if env == domain.PreviewBaseName {
			service.PreviewBase = &current
		} else {
			service.Environments[env] = current
		}
		st.Workers.Services[serviceID] = service
	}
	return rows.Err()
}

func readWorkerVersionR2(results pgx.BatchResults, st *domain.State) error {
	rows, err := results.Query()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var versionID, name, bucketID string
		if err := rows.Scan(&versionID, &name, &bucketID); err != nil {
			return err
		}
		version, exists := st.Workers.Versions[versionID]
		if !exists {
			continue
		}
		if version.R2Bindings == nil {
			version.R2Bindings = map[string]domain.WorkerR2Binding{}
		}
		version.R2Bindings[name] = domain.WorkerR2Binding{StoreID: st.Workers.Buckets[bucketID].Store, BucketID: bucketID, BucketName: st.Workers.Buckets[bucketID].Name}
		st.Workers.Versions[versionID] = version
	}
	return rows.Err()
}

type storageReader interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func loadVersionStorage(ctx context.Context, reader storageReader, versions map[string]domain.WorkerVersion) error {
	if len(versions) == 0 {
		return nil
	}
	ids := make([]string, 0, len(versions))
	for id := range versions {
		ids = append(ids, id)
	}
	rows, err := reader.Query(ctx, `SELECT b.version_id,b.name,b.bucket_id,k.name,k.store_id FROM krun_worker_version_r2 b JOIN krun_worker_buckets k ON k.bucket_id=b.bucket_id JOIN krun_worker_versions v ON v.version_id=b.version_id AND v.org_id=k.org_id WHERE b.version_id=ANY($1)`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var versionID, name, bucketID, bucketName, storeID string
		if err := rows.Scan(&versionID, &name, &bucketID, &bucketName, &storeID); err != nil {
			return err
		}
		version := versions[versionID]
		if version.R2Bindings == nil {
			version.R2Bindings = map[string]domain.WorkerR2Binding{}
		}
		version.R2Bindings[name] = domain.WorkerR2Binding{StoreID: storeID, BucketID: bucketID, BucketName: bucketName}
		versions[versionID] = version
	}
	return rows.Err()
}
