package control

import (
	"context"
	"github.com/jackc/pgx/v5"
)

// Runtime cold paths use indexed entity reads; they must not load IAM, build
// history, or any other tenant's metadata.
func (s *Store) runtimeRelease(ctx context.Context, id, pool string) (WorkerRelease, WorkerArtifact, error) {
	state := newWorkerState()
	if s.pool == nil {
		var rel WorkerRelease
		var art WorkerArtifact
		err := s.View(ctx, PartitionAll, func(st *State) error {
			r, ok := st.Workers.Releases[id]
			if !ok || !workerServiceAvailableInPool(st.Workers, r.Service, pool) || st.Resources[r.Service].ID == "" {
				return notFound()
			}
			a, ok := st.Workers.Artifacts[r.Artifact]
			if !ok || a.State != "finalized" {
				return notFound()
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
		return WorkerRelease{}, WorkerArtifact{}, err
	}
	err = readReleaseRows(rows, state)
	rows.Close()
	if err != nil {
		return WorkerRelease{}, WorkerArtifact{}, err
	}
	rel, ok := state.Releases[id]
	if !ok {
		return rel, WorkerArtifact{}, notFound()
	}
	art, err := s.runtimeArtifact(ctx, rel.Artifact, pool)
	return rel, art, err
}
func (s *Store) runtimeArtifact(ctx context.Context, id, pool string) (WorkerArtifact, error) {
	state := newWorkerState()
	if s.pool == nil {
		var art WorkerArtifact
		err := s.View(ctx, PartitionAll, func(st *State) error {
			a, ok := st.Workers.Artifacts[id]
			if !ok || a.State != "finalized" {
				return notFound()
			}
			for _, r := range st.Workers.Releases {
				if r.Artifact == id && workerServiceAvailableInPool(st.Workers, r.Service, pool) && st.Resources[r.Service].ID != "" {
					art = a
					return nil
				}
			}
			return forbidden()
		})
		return art, err
	}
	rows, err := s.pool.Query(ctx, `SELECT artifact_id,org_id,state,profile,archive_digest,manifest_digest,module_digest,compatibility_date,compatibility_flags,size_bytes,object_key,storage_location,bucket,created_at FROM krun_worker_artifacts a
 WHERE artifact_id=$1 AND state='finalized' AND EXISTS (
 SELECT 1 FROM krun_worker_releases rel JOIN krun_worker_service_placements p ON p.service_id=rel.service_id AND p.org_id=rel.org_id
 JOIN krun_resources r ON r.resource_id=p.service_id AND r.kind='service' AND r.org_id=p.org_id
 WHERE rel.artifact_id=a.artifact_id AND p.pool_id=$2 AND p.state IN ('preparing','ready','active'))`, id, pool)
	if err != nil {
		return WorkerArtifact{}, err
	}
	err = readArtifactRows(rows, state)
	rows.Close()
	if err != nil {
		return WorkerArtifact{}, err
	}
	art, ok := state.Artifacts[id]
	if !ok {
		return art, notFound()
	}
	return art, nil
}

// actorScope is shared by asynchronous re-authorization and request reads.
func (s *Store) actorScope(ctx context.Context, actor WorkerActor, service string) (context.Context, error) {
	if s.pool == nil {
		return ctx, nil
	}
	var org string
	err := s.pool.QueryRow(ctx, `SELECT org_id FROM krun_resources WHERE resource_id=$1 AND kind='service'`, service).Scan(&org)
	if err == pgx.ErrNoRows {
		return ctx, notFound()
	}
	if err != nil {
		return ctx, err
	}
	return withStateScope(ctx, stateReadScope{User: actor.User, Key: actor.Key, Organization: org, Service: service, Kind: "service", Limit: 1}), nil
}
