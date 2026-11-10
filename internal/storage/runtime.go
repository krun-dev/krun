package storage

import (
	"context"
	"database/sql"
	"errors"

	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/storage/models"
)

// Runtime cold paths use indexed entity reads; they must not load IAM, build
// history, or any other tenant's metadata.
func (s *Store) RuntimeVersion(ctx context.Context, id, pool string) (domain.WorkerVersion, domain.WorkerArtifact, error) {
	state := domain.NewWorkerState()

	rows, err := s.pool.Query(ctx, `SELECT rel.version_id,rel.org_id,rel.worker_id,rel.env_name,rel.artifact_id,rel.profile,rel.archive_digest,rel.module_digest,rel.compatibility_date,rel.compatibility_flags,rel.config_revision,rel.variables,rel.secret_digest,rel.created_at,rel.version_metadata,rel.assets_binding,rel.tag,rel.message,rel.created_by,rel.created_by_name,rel.configuration_id,rel.hyperdrive_bindings
 FROM krun_worker_versions rel WHERE version_id=$1 AND EXISTS (
 SELECT 1 FROM krun_worker_placements p JOIN krun_resources r ON r.resource_id=p.worker_id AND r.kind='worker' AND r.org_id=p.org_id
 WHERE p.worker_id=rel.worker_id AND p.org_id=rel.org_id AND p.pool_id=$2 AND p.state IN ('preparing','ready','active'))`, id, pool)
	if err != nil {
		return domain.WorkerVersion{}, domain.WorkerArtifact{}, err
	}
	err = readVersionRows(rows, state)
	rows.Close()
	if err != nil {
		return domain.WorkerVersion{}, domain.WorkerArtifact{}, err
	}
	if err := loadVersionStorage(ctx, s.pool, state.Versions); err != nil {
		return domain.WorkerVersion{}, domain.WorkerArtifact{}, err
	}
	rel, ok := state.Versions[id]
	if !ok {
		return rel, domain.WorkerArtifact{}, domain.NotFound()
	}
	art, err := s.RuntimeArtifact(ctx, rel.Artifact, pool)
	return rel, art, err
}

func (s *Store) RuntimeArtifact(ctx context.Context, id, pool string) (domain.WorkerArtifact, error) {
	state := domain.NewWorkerState()

	rows, err := s.pool.Query(ctx, `SELECT artifact_id,org_id,state,profile,archive_digest,manifest_digest,module_digest,compatibility_date,compatibility_flags,size_bytes,object_key,storage_location,bucket,created_at FROM krun_worker_artifacts a
 WHERE artifact_id=$1 AND state='finalized' AND EXISTS (
 SELECT 1 FROM krun_worker_versions rel JOIN krun_worker_placements p ON p.worker_id=rel.worker_id AND p.org_id=rel.org_id
 JOIN krun_resources r ON r.resource_id=p.worker_id AND r.kind='worker' AND r.org_id=p.org_id
 WHERE rel.artifact_id=a.artifact_id AND p.pool_id=$2 AND p.state IN ('preparing','ready','active'))`, id, pool)
	if err != nil {
		return domain.WorkerArtifact{}, err
	}
	err = readArtifactRows(rows, state)
	rows.Close()
	if err != nil {
		return domain.WorkerArtifact{}, err
	}
	art, ok := state.Artifacts[id]
	if !ok {
		return art, domain.NotFound()
	}
	return art, nil
}

// actorScope is shared by asynchronous re-authorization and request reads.
func (s *Store) ActorScope(ctx context.Context, actor domain.WorkerActor, worker string) (context.Context, error) {
	var resource models.Resource
	err := s.orm.NewSelect().Model(&resource).
		Column("org_id").
		Where("resource_id = ? AND kind = 'worker'", worker).
		Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return ctx, domain.NotFound()
	}
	if err != nil {
		return ctx, err
	}
	return WithReadScope(ctx, ReadScope{User: actor.User, Key: actor.Key, Organization: resource.OrgID, Worker: worker, Kind: "worker", Limit: 1}), nil
}
