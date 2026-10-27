package control

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
)

// Claiming is a compare-and-set on one row. Rows affected of 0 means another
// replica already moved it, which is a normal outcome, not an error. This
// replaces the advisory-lock leader election: replicas no longer serialize the
// whole reconcile, they just race per operation.
//
// A claimer that dies leaves claimed_by/claimed_at behind; the lease expiry
// lets another replica take the row over. All side effects downstream must be
// idempotent because the same operation can run twice.

const workerClaimLease = "60 seconds"

// claimBuild moves a build from an active state to building. It returns false
// when the build is already finished or claimed by a live replica.
func (s *Store) claimBuild(ctx context.Context, id, replica string) (bool, error) {
	if s.pool == nil {
		return true, nil
	}
	tag, err := s.pool.Exec(ctx, `UPDATE krun_worker_builds
		SET state='building', claimed_by=$2, claimed_at=now()
		WHERE build_id=$1 AND state IN ('queued','fetching')`, id, replica)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// claimOperation prepares a queued operation for validation.
func (s *Store) claimOperation(ctx context.Context, id, replica string) (bool, error) {
	if s.pool == nil {
		return true, nil
	}
	tag, err := s.pool.Exec(ctx, `UPDATE krun_worker_operations
		SET state='validating', claimed_by=$2, claimed_at=now()
		WHERE operation_id=$1 AND state='queued'`, id, replica)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// claimStaleOperation takes over a validating operation whose claimer died.
func (s *Store) claimStaleOperation(ctx context.Context, id, replica string) (bool, error) {
	if s.pool == nil {
		return false, nil
	}
	tag, err := s.pool.Exec(ctx, `UPDATE krun_worker_operations
		SET claimed_by=$2, claimed_at=now()
		WHERE operation_id=$1 AND state='validating'
		  AND claimed_at < now() - $3::interval`, id, replica, workerClaimLease)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// commitOperation publishes a validated release. The environment row must still
// carry the revision the operation validated against, so a configuration edit
// that landed in between makes this a no-op instead of an overwrite.
func (s *Store) commitOperation(ctx context.Context, op WorkerOperation) (bool, error) {
	if s.pool == nil {
		return false, nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)

	// Supersede any earlier committed operation for the same environment; it is
	// still converging and its release is about to be replaced.
	if _, err = tx.Exec(ctx, `UPDATE krun_worker_operations
		SET state='superseded', error='Replaced by '||$3, finished_at=now()
		WHERE service_id=$1 AND env_name=$2 AND operation_id<>$3 AND state='committed'`,
		op.Service, op.Environment, op.ID); err != nil {
		return false, err
	}
	tag, err := tx.Exec(ctx, `UPDATE krun_worker_environments
		SET release_id=$4, release_revision=release_revision+1, updated_at=now()
		WHERE service_id=$1 AND env_name=$2 AND release_revision=$3 AND enabled`,
		op.Service, op.Environment, op.ExpectedRevision, op.Release)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 0 {
		// The environment moved on: fail the operation, which is what the
		// previous single-writer version also did on a revision conflict.
		if _, err = tx.Exec(ctx, `UPDATE krun_worker_operations
			SET state='failed', error='Release or configuration revision conflict', finished_at=now()
			WHERE operation_id=$1 AND state IN ('queued','validating')`, op.ID); err != nil {
			return false, err
		}
		return false, tx.Commit(ctx)
	}
	var revision int
	if err = tx.QueryRow(ctx, `SELECT release_revision FROM krun_worker_environments WHERE service_id=$1 AND env_name=$2`, op.Service, op.Environment).Scan(&revision); err != nil {
		return false, err
	}
	// Steps 0-2 (artifact, candidate, commit) are finished by the time the
	// release is durable; step 3 stays running until acks converge.
	tag, err = tx.Exec(ctx, `UPDATE krun_worker_operations
		SET state='committed', error='', release_revision=$2, edge_revision=0, finished_at=NULL,
		    steps = (SELECT COALESCE(jsonb_agg(
		        CASE step->>'name'
		            WHEN 'artifact'    THEN jsonb_set(step, '{state}', '"done"')
		            WHEN 'candidate'   THEN jsonb_set(step, '{state}', '"done"')
		            WHEN 'commit'      THEN jsonb_set(step, '{state}', '"done"')
		            WHEN 'synchronize' THEN jsonb_set(step, '{state}', '"running"')
		            ELSE step END), steps)
		        FROM jsonb_array_elements(steps) AS step)
		WHERE operation_id=$1 AND state IN ('queued','validating')`, op.ID, revision)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 0 {
		return false, tx.Commit(ctx)
	}
	return true, tx.Commit(ctx)
}

// confirmOperation records convergence once every serving Pod has acked the
// edge revision. It also appends the release history entry.
func (s *Store) confirmOperation(ctx context.Context, op WorkerOperation) error {
	if s.pool == nil {
		return nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE krun_worker_operations
		SET state='available', error='', finished_at=now(),
		    steps = jsonb_set(steps, '{3,state}', '"done"')
		WHERE operation_id=$1 AND state='committed'`, op.ID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return tx.Commit(ctx)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO krun_worker_history (service_id, env_name, revision, release_id, operation_id, kind, created_at)
		SELECT service_id, env_name, release_revision, release_id, operation_id, kind, now()
		FROM krun_worker_operations WHERE operation_id=$1
		ON CONFLICT (service_id, env_name, revision) DO NOTHING`, op.ID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE krun_worker_environments
		SET confirmed_release_id = (SELECT release_id FROM krun_worker_operations WHERE operation_id=$1), updated_at=now()
		WHERE service_id=$2 AND env_name=$3 AND release_id=(SELECT release_id FROM krun_worker_operations WHERE operation_id=$1)`,
		op.ID, op.Service, op.Environment); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// setOperationEdgeRevision records the published config revision. Zero means it
// has not been published yet.
func (s *Store) setOperationEdgeRevision(ctx context.Context, id string, revision int64) error {
	if s.pool == nil {
		return nil
	}
	_, err := s.pool.Exec(ctx, `UPDATE krun_worker_operations SET edge_revision=$2 WHERE operation_id=$1 AND state='committed' AND edge_revision<>$2`, id, revision)
	return err
}

func (s *Store) setOperationState(ctx context.Context, id, from, to, message string) error {
	if s.pool == nil {
		return nil
	}
	_, err := s.pool.Exec(ctx, `UPDATE krun_worker_operations
		SET state=$3, error=$4, finished_at=CASE WHEN $3 IN ('failed','superseded') THEN now() ELSE finished_at END
		WHERE operation_id=$1 AND state=$2`, id, from, to, message)
	return err
}

func (s *Store) noteOperationMessage(ctx context.Context, id, message string) error {
	if s.pool == nil {
		return nil
	}
	_, err := s.pool.Exec(ctx, `UPDATE krun_worker_operations SET error=$2
		WHERE operation_id=$1 AND state NOT IN ('succeeded','failed','available','superseded')`, id, message)
	return err
}

// setBuildSource records the object-storage key once inline files have been
// packed into a bundle, and drops the inlined copy.
func (s *Store) setBuildSource(ctx context.Context, id, key string) error {
	if s.pool == nil {
		return nil
	}
	_, err := s.pool.Exec(ctx, `UPDATE krun_worker_build_private SET source_key=$2, files=NULL WHERE build_id=$1`, id, key)
	return err
}

// failOperation marks an in-flight operation failed and stamps whichever step
// was running, matching what the in-memory helper used to do.
func (s *Store) failOperation(ctx context.Context, id, message string) error {
	if s.pool == nil {
		return nil
	}
	_, err := s.pool.Exec(ctx, `UPDATE krun_worker_operations
		SET state='failed', error=$2, finished_at=now(),
		    steps = COALESCE((
		        SELECT jsonb_agg(CASE WHEN step->>'state' = 'running'
		            THEN jsonb_set(step, '{state}', '"failed"') || jsonb_build_object('detail', $2::text)
		            ELSE step END)
		        FROM jsonb_array_elements(steps) AS step
		    ), steps)
		WHERE operation_id=$1 AND state NOT IN ('succeeded','failed','available','superseded')`, id, message)
	return err
}

// finishBuild records a terminal build state together with its log. Only one
// writer wins because the state transition is conditional.
func (s *Store) finishBuild(ctx context.Context, id, state, message, artifact, log string) error {
	if s.pool == nil {
		return nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE krun_worker_builds
		SET state=$2, error=$3, artifact_id=$4, finished_at=now()
		WHERE build_id=$1 AND state NOT IN ('succeeded','failed')`, id, state, message, artifact)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return tx.Commit(ctx)
	}
	if _, err = tx.Exec(ctx, `UPDATE krun_worker_build_private SET log=$2 WHERE build_id=$1`, id, log); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// finishBuildWithArtifact is finishBuild plus an artifact insert in one batch,
// used by the local builder which produces both at once.
func (s *Store) finishBuildWithArtifact(ctx context.Context, id, state, message string, artifact WorkerArtifact, log string) error {
	if s.pool == nil {
		return nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `INSERT INTO krun_worker_artifacts (artifact_id, org_id, state, archive_digest, manifest_digest, module_digest, size_bytes, object_key, storage_location, bucket, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT (artifact_id) DO NOTHING`,
		artifact.ID, artifact.Organization, artifact.State, artifact.ArchiveDigest, artifact.ManifestDigest, artifact.ModuleDigest, artifact.Size, artifact.ObjectKey, artifact.StorageLocation, artifact.Bucket, artifact.Created); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE krun_worker_builds
		SET state=$2, error=$3, artifact_id=$4, finished_at=now()
		WHERE build_id=$1 AND state NOT IN ('succeeded','failed')`, id, state, message, artifact.ID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return tx.Commit(ctx)
	}
	if _, err = tx.Exec(ctx, `UPDATE krun_worker_build_private SET log=$2 WHERE build_id=$1`, id, log); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// setBuildState applies a non-terminal transition, e.g. queued → fetching.
func (s *Store) setBuildState(ctx context.Context, id, from, to string) error {
	if s.pool == nil {
		return nil
	}
	_, err := s.pool.Exec(ctx, `UPDATE krun_worker_builds SET state=$3 WHERE build_id=$1 AND state=$2`, id, from, to)
	return err
}

// pendingWork returns the unfinished operations and builds for one reconcile
// cycle. Both queries use partial indexes, so an idle control plane scans no
// rows instead of deserializing a whole document.
func (s *Store) pendingWork(ctx context.Context, limit int) (WorkerState, error) {
	state := WorkerState{
		Builds:           map[string]WorkerBuild{},
		BuildPrivate:     map[string]WorkerBuildPrivate{},
		Artifacts:        map[string]WorkerArtifact{},
		Releases:         map[string]WorkerRelease{},
		Operations:       map[string]WorkerOperation{},
		OperationPrivate: map[string]WorkerOperationPrivate{},
		Histories:        map[string][]WorkerHistory{},
		Idempotency:      map[string]WorkerIdempotency{},
		Services:         map[string]WorkerService{},
	}
	if s.pool == nil {
		return state, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT operation_id, kind, org_id, service_id, env_name, state, build_id, release_id, expected_revision, config_revision, edge_revision, release_revision, error, steps, actor_user_id, actor_key_id, actor_auth_version, created_at, finished_at
		FROM krun_worker_operations
		WHERE state NOT IN ('succeeded','failed','available','superseded')
		ORDER BY created_at LIMIT $1`, limit)
	if err != nil {
		return state, err
	}
	ops, err := scanOperations(rows)
	rows.Close()
	if err != nil {
		return state, err
	}
	for _, op := range ops {
		state.Operations[op.Operation.ID] = op.Operation
		state.OperationPrivate[op.Operation.ID] = WorkerOperationPrivate{Actor: op.Actor}
	}
	rows, err = s.pool.Query(ctx, `SELECT build_id, org_id, service_id, env_name, state, entrypoint, source_digest, profile, job, artifact_id, error, repository, assets, created_at, finished_at
		FROM krun_worker_builds
		WHERE state NOT IN ('succeeded','failed')
		ORDER BY created_at LIMIT $1`, limit)
	if err != nil {
		return state, err
	}
	builds, err := scanBuilds(rows)
	rows.Close()
	if err != nil {
		return state, err
	}
	for _, b := range builds {
		state.Builds[b.ID] = b
	}
	if len(state.Builds) > 0 {
		// The build actor and its source live in the private table; without them
		// re-authorization sees an empty actor and rejects every build.
		ids := make([]string, 0, len(state.Builds))
		for id := range state.Builds {
			ids = append(ids, id)
		}
		rows, err = s.pool.Query(ctx, `SELECT build_id, actor_user_id, actor_key_id, actor_auth_version, source_key, files, log
			FROM krun_worker_build_private WHERE build_id = ANY($1)`, ids)
		if err != nil {
			return state, err
		}
		if err = readWorkerBuildPrivateRows(rows, &state); err != nil {
			rows.Close()
			return state, err
		}
		rows.Close()
	}
	if len(state.Operations) > 0 {
		// processOperation needs the release and artifact it is validating.
		releaseIDs := []string{}
		for _, op := range state.Operations {
			if op.Release != "" {
				releaseIDs = append(releaseIDs, op.Release)
			}
		}
		if len(releaseIDs) > 0 {
			rows, err = s.pool.Query(ctx, `SELECT release_id, org_id, service_id, env_name, artifact_id, archive_digest, module_digest, config_revision, variables, pool, created_at
				FROM krun_worker_releases WHERE release_id = ANY($1)`, releaseIDs)
			if err != nil {
				return state, err
			}
			if err = readReleaseRows(rows, &state); err != nil {
				rows.Close()
				return state, err
			}
			rows.Close()
		}
		artifactIDs := []string{}
		for _, release := range state.Releases {
			if release.Artifact != "" {
				artifactIDs = append(artifactIDs, release.Artifact)
			}
		}
		if len(artifactIDs) > 0 {
			rows, err = s.pool.Query(ctx, `SELECT artifact_id, org_id, state, archive_digest, manifest_digest, module_digest, size_bytes, object_key, storage_location, bucket, created_at
				FROM krun_worker_artifacts WHERE artifact_id = ANY($1)`, artifactIDs)
			if err != nil {
				return state, err
			}
			if err = readArtifactRows(rows, &state); err != nil {
				rows.Close()
				return state, err
			}
			rows.Close()
		}
	}
	return state, nil
}

func readWorkerBuildPrivateRows(rows pgx.Rows, state *WorkerState) error {
	for rows.Next() {
		var id string
		var p WorkerBuildPrivate
		var files []byte
		if err := rows.Scan(&id, &p.Actor.User, &p.Actor.Key, &p.Actor.AuthVersion, &p.SourceKey, &files, &p.Log); err != nil {
			return err
		}
		if len(files) > 0 {
			if err := json.Unmarshal(files, &p.Files); err != nil {
				return err
			}
		}
		state.BuildPrivate[id] = p
	}
	return rows.Err()
}

func readReleaseRows(rows pgx.Rows, state *WorkerState) error {
	for rows.Next() {
		var r WorkerRelease
		var variables []byte
		if err := rows.Scan(&r.ID, &r.Organization, &r.Service, &r.Environment, &r.Artifact, &r.ArchiveDigest, &r.ModuleDigest, &r.ConfigRevision, &variables, &r.Pool, &r.Created); err != nil {
			return err
		}
		r.Variables = map[string]string{}
		if len(variables) > 0 {
			if err := json.Unmarshal(variables, &r.Variables); err != nil {
				return err
			}
		}
		state.Releases[r.ID] = r
	}
	return rows.Err()
}

func readArtifactRows(rows pgx.Rows, state *WorkerState) error {
	for rows.Next() {
		var a WorkerArtifact
		if err := rows.Scan(&a.ID, &a.Organization, &a.State, &a.ArchiveDigest, &a.ManifestDigest, &a.ModuleDigest, &a.Size, &a.ObjectKey, &a.StorageLocation, &a.Bucket, &a.Created); err != nil {
			return err
		}
		state.Artifacts[a.ID] = a
	}
	return rows.Err()
}

func scanOperations(rows pgx.Rows) ([]struct {
	Operation WorkerOperation
	Actor     WorkerActor
}, error) {
	out := []struct {
		Operation WorkerOperation
		Actor     WorkerActor
	}{}
	for rows.Next() {
		var item struct {
			Operation WorkerOperation
			Actor     WorkerActor
		}
		op := &item.Operation
		var steps []byte
		if err := rows.Scan(&op.ID, &op.Kind, &op.Organization, &op.Service, &op.Environment, &op.State, &op.Build, &op.Release, &op.ExpectedRevision, &op.ConfigRevision, &op.EdgeRevision, &op.ReleaseRevision, &op.Error, &steps, &item.Actor.User, &item.Actor.Key, &item.Actor.AuthVersion, &op.Created, &op.Finished); err != nil {
			return nil, err
		}
		if len(steps) > 0 {
			if err := json.Unmarshal(steps, &op.Steps); err != nil {
				return nil, err
			}
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func scanBuilds(rows pgx.Rows) ([]WorkerBuild, error) {
	out := []WorkerBuild{}
	for rows.Next() {
		var b WorkerBuild
		var repository, assets []byte
		if err := rows.Scan(&b.ID, &b.Organization, &b.Service, &b.Environment, &b.State, &b.Entrypoint, &b.SourceDigest, &b.Profile, &b.Job, &b.Artifact, &b.Error, &repository, &assets, &b.Created, &b.Finished); err != nil {
			return nil, err
		}
		if len(repository) > 0 {
			var src WorkerBuildRepository
			if err := json.Unmarshal(repository, &src); err != nil {
				return nil, err
			}
			b.Repository = &src
		}
		if len(assets) > 0 {
			var a WorkerAssetsConfig
			if err := json.Unmarshal(assets, &a); err != nil {
				return nil, err
			}
			b.Assets = &a
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
