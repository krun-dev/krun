package storage

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/krun-dev/krun/internal/domain"
)

// Runtime cold paths use indexed entity reads; they must not load IAM, build
// history, or any other tenant's metadata.
func (s *Store) RuntimeRelease(ctx context.Context, id, pool string) (domain.WorkerRelease, domain.WorkerArtifact, error) {
	state := domain.NewWorkerState()
	if s.pool == nil {
		var rel domain.WorkerRelease
		var art domain.WorkerArtifact
		err := s.View(ctx, PartitionAll, func(st *domain.State) error {
			r, ok := st.Workers.Releases[id]
			if !ok || !domain.WorkerServiceAvailableInPool(st.Workers, r.Service, pool) || st.Resources[r.Service].ID == "" {
				return domain.NotFound()
			}
			a, ok := st.Workers.Artifacts[r.Artifact]
			if !ok || a.State != "finalized" {
				return domain.NotFound()
			}
			rel, art = r, a
			return nil
		})
		return rel, art, err
	}
	rows, err := s.pool.Query(ctx, `SELECT rel.release_id,rel.org_id,rel.service_id,rel.env_name,rel.artifact_id,rel.profile,rel.archive_digest,rel.module_digest,rel.compatibility_date,rel.compatibility_flags,rel.config_revision,rel.variables,rel.secret_digest,rel.created_at
 FROM krun_worker_releases rel WHERE release_id=$1 AND EXISTS (
 SELECT 1 FROM krun_worker_service_placements p JOIN krun_resources r ON r.resource_id=p.service_id AND r.kind='service' AND r.org_id=p.org_id
 WHERE p.service_id=rel.service_id AND p.org_id=rel.org_id AND p.pool_id=$2 AND p.state IN ('preparing','ready','active'))`, id, pool)
	if err != nil {
		return domain.WorkerRelease{}, domain.WorkerArtifact{}, err
	}
	err = readReleaseRows(rows, state)
	rows.Close()
	if err != nil {
		return domain.WorkerRelease{}, domain.WorkerArtifact{}, err
	}
	rel, ok := state.Releases[id]
	if !ok {
		return rel, domain.WorkerArtifact{}, domain.NotFound()
	}
	art, err := s.RuntimeArtifact(ctx, rel.Artifact, pool)
	return rel, art, err
}

func (s *Store) RuntimeArtifact(ctx context.Context, id, pool string) (domain.WorkerArtifact, error) {
	state := domain.NewWorkerState()
	if s.pool == nil {
		var art domain.WorkerArtifact
		err := s.View(ctx, PartitionAll, func(st *domain.State) error {
			a, ok := st.Workers.Artifacts[id]
			if !ok || a.State != "finalized" {
				return domain.NotFound()
			}
			for _, r := range st.Workers.Releases {
				if r.Artifact == id && domain.WorkerServiceAvailableInPool(st.Workers, r.Service, pool) && st.Resources[r.Service].ID != "" {
					art = a
					return nil
				}
			}
			return domain.Forbidden()
		})
		return art, err
	}
	rows, err := s.pool.Query(ctx, `SELECT artifact_id,org_id,state,profile,archive_digest,manifest_digest,module_digest,compatibility_date,compatibility_flags,size_bytes,object_key,storage_location,bucket,created_at FROM krun_worker_artifacts a
 WHERE artifact_id=$1 AND state='finalized' AND EXISTS (
 SELECT 1 FROM krun_worker_releases rel JOIN krun_worker_service_placements p ON p.service_id=rel.service_id AND p.org_id=rel.org_id
 JOIN krun_resources r ON r.resource_id=p.service_id AND r.kind='service' AND r.org_id=p.org_id
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
func (s *Store) ActorScope(ctx context.Context, actor domain.WorkerActor, service string) (context.Context, error) {
	if s.pool == nil {
		return ctx, nil
	}
	var org string
	err := s.pool.QueryRow(ctx, `SELECT org_id FROM krun_resources WHERE resource_id=$1 AND kind='service'`, service).Scan(&org)
	if err == pgx.ErrNoRows {
		return ctx, domain.NotFound()
	}
	if err != nil {
		return ctx, err
	}
	return WithReadScope(ctx, ReadScope{User: actor.User, Key: actor.Key, Organization: org, Service: service, Kind: "service", Limit: 1}), nil
}
