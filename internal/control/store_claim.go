package control

import (
	"context"
	"encoding/json"
	"errors"

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

var errArtifactBeingCollected = errors.New("artifact is being garbage-collected")
var errBuildClaimLost = errors.New("build claim is no longer owned")

// claimBuild moves a queued build to building, refreshes the current owner's
// lease, or takes over a lease whose owner stopped making progress. A
// controller calls this before every externally-visible build side effect.
func (s *Store) claimBuild(ctx context.Context, id, replica string) (bool, error) {
	if s.pool == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.memory == nil {
			return false, errors.New("store is not initialized")
		}
		build, ok := s.memory.Workers.Builds[id]
		if !ok || workerBuildTerminal(build.State) {
			return false, nil
		}
		claimed := now()
		build.State, build.ClaimedBy, build.ClaimedAt = "building", replica, &claimed
		s.memory.Workers.Builds[id] = build
		return true, nil
	}
	tag, err := s.pool.Exec(ctx, `UPDATE krun_worker_builds
		SET state='building', claimed_by=$2, claimed_at=now()
		WHERE build_id=$1 AND (
			state='queued'
			OR (state IN ('fetching','building') AND (
				claimed_by=$2 OR claimed_at IS NULL OR claimed_at < now() - $3::interval
			))
		)`, id, replica, workerClaimLease)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (s *Store) renewBuildClaim(ctx context.Context, id, replica string) (bool, error) {
	if s.pool == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.memory == nil {
			return false, errors.New("store is not initialized")
		}
		build, ok := s.memory.Workers.Builds[id]
		if !ok || build.ClaimedBy != replica || (build.State != "fetching" && build.State != "building") {
			return false, nil
		}
		claimed := now()
		build.ClaimedAt = &claimed
		s.memory.Workers.Builds[id] = build
		return true, nil
	}
	tag, err := s.pool.Exec(ctx, `UPDATE krun_worker_builds SET claimed_at=now()
		WHERE build_id=$1 AND claimed_by=$2 AND state IN ('fetching','building')`, id, replica)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// setClaimedBuildState applies a controller transition only while this replica
// still owns the build lease. Generic snapshot writes are intentionally not
// used for controller progress because their read happens before the write
// transaction and can otherwise revive a terminal build.
func (s *Store) setClaimedBuildState(ctx context.Context, id, replica, from, to string) (bool, error) {
	if s.pool == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.memory == nil {
			return false, errors.New("store is not initialized")
		}
		build, ok := s.memory.Workers.Builds[id]
		if !ok || build.ClaimedBy != replica || build.State != from {
			return false, nil
		}
		build.State = to
		s.memory.Workers.Builds[id] = build
		return true, nil
	}
	tag, err := s.pool.Exec(ctx, `UPDATE krun_worker_builds SET state=$4
		WHERE build_id=$1 AND claimed_by=$2 AND state=$3`, id, replica, from, to)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (s *Store) setClaimedBuildRepository(ctx context.Context, id, replica string, source WorkerBuildRepository) (bool, error) {
	raw, err := json.Marshal(source)
	if err != nil {
		return false, err
	}
	if s.pool == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.memory == nil {
			return false, errors.New("store is not initialized")
		}
		build, ok := s.memory.Workers.Builds[id]
		if !ok || build.ClaimedBy != replica || (build.State != "fetching" && build.State != "building") {
			return false, nil
		}
		build.Repository = &source
		s.memory.Workers.Builds[id] = build
		return true, nil
	}
	tag, err := s.pool.Exec(ctx, `UPDATE krun_worker_builds SET repository=$3
		WHERE build_id=$1 AND claimed_by=$2 AND state IN ('fetching','building')`, id, replica, raw)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// storeClaimedRepositoryBuild atomically persists the fetched source metadata,
// source reservation and private log while this replica still owns an active
// build. A terminal transition or lease takeover makes the whole write a no-op
// with errBuildClaimLost instead of restoring stale state.
func (s *Store) storeClaimedRepositoryBuild(ctx context.Context, id, replica string, build WorkerBuild, private WorkerBuildPrivate) error {
	if s.pool == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.memory == nil {
			return errors.New("store is not initialized")
		}
		current, ok := s.memory.Workers.Builds[id]
		if !ok || current.ClaimedBy != replica || workerBuildTerminal(current.State) {
			return errBuildClaimLost
		}
		build.ClaimedBy, build.ClaimedAt = current.ClaimedBy, current.ClaimedAt
		s.memory.Workers.Builds[id] = build
		s.memory.Workers.BuildPrivate[id] = private
		return nil
	}
	repository, err := json.Marshal(build.Repository)
	if err != nil {
		return err
	}
	var assets []byte
	if build.Assets != nil {
		assets, err = json.Marshal(build.Assets)
		if err != nil {
			return err
		}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE krun_worker_builds
		SET state=$3, entrypoint=$4, source_digest=$5, profile=$6, job=$7,
		    artifact_id=$8, error=$9, repository=$10, assets=$11, finished_at=$12
		WHERE build_id=$1 AND claimed_by=$2 AND state IN ('fetching','building')`,
		id, replica, build.State, build.Entrypoint, build.SourceDigest, build.Profile,
		build.Job, build.Artifact, build.Error, repository, assets, build.Finished)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errBuildClaimLost
	}
	var oldSource string
	if err = tx.QueryRow(ctx, `SELECT source_key FROM krun_worker_build_private WHERE build_id=$1`, id).Scan(&oldSource); err != nil {
		return err
	}
	if private.SourceKey != "" && private.SourceKey != oldSource {
		if err = registerGeneratedBuildSourceTx(ctx, tx, id, build.Organization, private.SourceKey); err != nil {
			return err
		}
	}
	var files []byte
	if private.Files != nil {
		files, err = json.Marshal(private.Files)
		if err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE krun_worker_build_private
		SET actor_user_id=$2, actor_key_id=$3, actor_auth_version=$4,
		    source_key=$5, files=$6, log=$7 WHERE build_id=$1`, id,
		private.Actor.User, private.Actor.Key, private.Actor.AuthVersion,
		private.SourceKey, files, private.Log); err != nil {
		return err
	}
	return tx.Commit(ctx)
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

// claimStaleOperation refreshes the current owner's validating lease or takes
// over one whose claimer died.
func (s *Store) claimStaleOperation(ctx context.Context, id, replica string) (bool, error) {
	if s.pool == nil {
		return false, nil
	}
	tag, err := s.pool.Exec(ctx, `UPDATE krun_worker_operations
		SET claimed_by=$2, claimed_at=now()
		WHERE operation_id=$1 AND state='validating'
		  AND (claimed_by=$2 OR claimed_at IS NULL OR claimed_at < now() - $3::interval)`, id, replica, workerClaimLease)
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

	// Win this operation before changing the environment. The transient state
	// is visible only inside this transaction, so a crash rolls it back and a
	// concurrent committer cannot publish a release for an operation it lost.
	tag, err := tx.Exec(ctx, `UPDATE krun_worker_operations SET state='committing'
		WHERE operation_id=$1 AND state IN ('queued','validating')`, op.ID)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 0 {
		return false, tx.Commit(ctx)
	}
	tag, err = tx.Exec(ctx, `UPDATE krun_worker_environments
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
			WHERE operation_id=$1 AND state='committing'`, op.ID); err != nil {
			return false, err
		}
		return false, tx.Commit(ctx)
	}
	if err = bumpWorkerTopologyGeneration(ctx, tx); err != nil {
		return false, err
	}
	// Supersede an earlier committed operation only after this release has won
	// the environment CAS. A stale operation must not cancel live convergence.
	if _, err = tx.Exec(ctx, `UPDATE krun_worker_operations
		SET state='superseded', error='Replaced by '||$3, finished_at=now()
		WHERE service_id=$1 AND env_name=$2 AND operation_id<>$3 AND state='committed'`,
		op.Service, op.Environment, op.ID); err != nil {
		return false, err
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
		WHERE operation_id=$1 AND state='committing'`, op.ID, revision)
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

// setOperationEdgeRevision records the first published config revision that
// contains this operation's commit. That revision is a durable lower bound for
// convergence: later unrelated publishes must not move an older operation's
// acknowledgement target forward forever.
func (s *Store) setOperationEdgeRevision(ctx context.Context, id string, revision int64) (int64, error) {
	if s.pool == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.memory == nil {
			return 0, errors.New("store is not initialized")
		}
		op, ok := s.memory.Workers.Operations[id]
		if !ok || op.State != "committed" {
			return 0, nil
		}
		if op.EdgeRevision == 0 {
			op.EdgeRevision = int(revision)
			s.memory.Workers.Operations[id] = op
		}
		return int64(op.EdgeRevision), nil
	}
	tag, err := s.pool.Exec(ctx, `UPDATE krun_worker_operations SET edge_revision=$2
		WHERE operation_id=$1 AND state='committed' AND edge_revision=0`, id, revision)
	if err != nil {
		return 0, err
	}
	var stored int64
	if err = s.pool.QueryRow(ctx, `SELECT edge_revision FROM krun_worker_operations
		WHERE operation_id=$1`, id).Scan(&stored); err != nil {
		if errors.Is(err, pgx.ErrNoRows) && tag.RowsAffected() == 0 {
			return 0, nil
		}
		return 0, err
	}
	return stored, nil
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
		return s.registerGeneratedBuildSource(ctx, id, "", key)
	}
	var organization string
	if err := s.pool.QueryRow(ctx, `SELECT org_id FROM krun_worker_builds WHERE build_id=$1`, id).Scan(&organization); err != nil {
		return err
	}
	return s.registerGeneratedBuildSource(ctx, id, organization, key)
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
	return s.finishBuildOwned(ctx, id, "", state, message, artifact, log)
}

func (s *Store) finishClaimedBuild(ctx context.Context, id, replica, state, message, artifact, log string) error {
	return s.finishBuildOwned(ctx, id, replica, state, message, artifact, log)
}

func (s *Store) finishBuildOwned(ctx context.Context, id, replica, state, message, artifact, log string) error {
	if !workerBuildTerminal(state) {
		return errors.New("finishBuild requires a terminal state")
	}
	if s.pool == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.memory == nil {
			return errors.New("store is not initialized")
		}
		build, ok := s.memory.Workers.Builds[id]
		if !ok || workerBuildTerminal(build.State) || (replica != "" && build.ClaimedBy != replica) {
			return nil
		}
		build.State, build.Error, build.Artifact = state, message, artifact
		finished := now()
		build.Finished = &finished
		s.memory.Workers.Builds[id] = build
		private := s.memory.Workers.BuildPrivate[id]
		private.Log = log
		s.memory.Workers.BuildPrivate[id] = private
		return nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var service string
	err = tx.QueryRow(ctx, `UPDATE krun_worker_builds
		SET state=$2, error=$3, artifact_id=$4, finished_at=now()
		WHERE build_id=$1 AND state NOT IN ('succeeded','failed')
		  AND ($5='' OR claimed_by=$5)
		RETURNING service_id`, id, state, message, artifact, replica).Scan(&service)
	if errors.Is(err, pgx.ErrNoRows) {
		return tx.Commit(ctx)
	}
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE krun_worker_services
		SET active_build_count=GREATEST(active_build_count-1,0), updated_at=now()
		WHERE service_id=$1`, service); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE krun_worker_build_private SET log=$2 WHERE build_id=$1`, id, log); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// reserveBuildArtifact creates a durable build reference before S3 publication.
// Incrementing use_revision on the artifact row races directly with the GC
// claimant's CAS; whichever wins prevents the other side from deleting or
// reusing the same content-addressed object underneath an active build.
func (s *Store) reserveBuildArtifact(ctx context.Context, id, replica string, artifact WorkerArtifact) error {
	if s.pool == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.memory == nil {
			return errors.New("store is not initialized")
		}
		existing, ok := s.memory.Workers.Artifacts[artifact.ID]
		if ok && existing.State == "deleting" {
			return errArtifactBeingCollected
		}
		if !ok {
			existing = artifact
			existing.State = "staging"
		}
		existing.UseRevision++
		s.memory.Workers.Artifacts[artifact.ID] = existing
		build, ok := s.memory.Workers.Builds[id]
		if !ok || build.State == "succeeded" || build.State == "failed" || build.ClaimedBy != replica {
			return errors.New("build is no longer active")
		}
		build.Artifact = artifact.ID
		s.memory.Workers.Builds[id] = build
		return nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `INSERT INTO krun_worker_artifacts
		(artifact_id, org_id, state, archive_digest, manifest_digest, module_digest, size_bytes, object_key, storage_location, bucket, created_at)
		VALUES ($1,$2,'staging',$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (artifact_id) DO NOTHING`, artifact.ID, artifact.Organization,
		artifact.ArchiveDigest, artifact.ManifestDigest, artifact.ModuleDigest,
		artifact.Size, artifact.ObjectKey, artifact.StorageLocation, artifact.Bucket, artifact.Created); err != nil {
		return err
	}
	var state string
	err = tx.QueryRow(ctx, `UPDATE krun_worker_artifacts SET use_revision=use_revision+1
		WHERE artifact_id=$1 AND state IN ('staging','finalized')
		  AND org_id=$2 AND archive_digest=$3 AND manifest_digest=$4 AND module_digest=$5
		  AND size_bytes=$6 AND object_key=$7 AND storage_location=$8 AND bucket=$9
		RETURNING state`, artifact.ID, artifact.Organization, artifact.ArchiveDigest,
		artifact.ManifestDigest, artifact.ModuleDigest, artifact.Size, artifact.ObjectKey,
		artifact.StorageLocation, artifact.Bucket).Scan(&state)
	if errors.Is(err, pgx.ErrNoRows) {
		return errArtifactBeingCollected
	}
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE krun_worker_builds SET artifact_id=$2
		WHERE build_id=$1 AND state NOT IN ('succeeded','failed') AND claimed_by=$3`, id, artifact.ID, replica)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("build is no longer active")
	}
	return tx.Commit(ctx)
}

// finishBuildWithArtifact finalizes a previously reserved artifact and the
// build in one transaction.
func (s *Store) finishBuildWithArtifact(ctx context.Context, id, state, message string, artifact WorkerArtifact, log string) error {
	return s.finishBuildWithArtifactOwned(ctx, id, "", state, message, artifact, log)
}

func (s *Store) finishClaimedBuildWithArtifact(ctx context.Context, id, replica, state, message string, artifact WorkerArtifact, log string) error {
	return s.finishBuildWithArtifactOwned(ctx, id, replica, state, message, artifact, log)
}

func (s *Store) finishBuildWithArtifactOwned(ctx context.Context, id, replica, state, message string, artifact WorkerArtifact, log string) error {
	if !workerBuildTerminal(state) {
		return errors.New("finishBuildWithArtifact requires a terminal state")
	}
	if s.pool == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.memory == nil {
			return errors.New("store is not initialized")
		}
		build, ok := s.memory.Workers.Builds[id]
		if !ok || workerBuildTerminal(build.State) || (replica != "" && build.ClaimedBy != replica) {
			return nil
		}
		stored, ok := s.memory.Workers.Artifacts[artifact.ID]
		if !ok || (stored.State != "staging" && stored.State != "finalized") {
			return errArtifactBeingCollected
		}
		stored.State = "finalized"
		s.memory.Workers.Artifacts[artifact.ID] = stored
		build.State, build.Error, build.Artifact = state, message, artifact.ID
		finished := now()
		build.Finished = &finished
		s.memory.Workers.Builds[id] = build
		private := s.memory.Workers.BuildPrivate[id]
		private.Log = log
		s.memory.Workers.BuildPrivate[id] = private
		return nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE krun_worker_artifacts
		SET state='finalized', gc_claimed_by='', gc_claimed_at=NULL
		WHERE artifact_id=$1 AND state IN ('staging','finalized')
		  AND org_id=$2 AND archive_digest=$3 AND manifest_digest=$4 AND module_digest=$5
		  AND size_bytes=$6 AND object_key=$7 AND storage_location=$8 AND bucket=$9`,
		artifact.ID, artifact.Organization, artifact.ArchiveDigest, artifact.ManifestDigest,
		artifact.ModuleDigest, artifact.Size, artifact.ObjectKey, artifact.StorageLocation, artifact.Bucket)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errArtifactBeingCollected
	}
	var service string
	err = tx.QueryRow(ctx, `UPDATE krun_worker_builds
		SET state=$2, error=$3, artifact_id=$4, finished_at=now()
		WHERE build_id=$1 AND state NOT IN ('succeeded','failed')
		  AND ($5='' OR claimed_by=$5)
		RETURNING service_id`, id, state, message, artifact.ID, replica).Scan(&service)
	if errors.Is(err, pgx.ErrNoRows) {
		if replica != "" {
			return nil
		}
		return tx.Commit(ctx)
	}
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE krun_worker_services
		SET active_build_count=GREATEST(active_build_count-1,0), updated_at=now()
		WHERE service_id=$1`, service); err != nil {
		return err
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
		Placements:       map[string]WorkerPlacement{},
		SecretVersions:   map[string]WorkerSecretVersion{},
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
	rows, err = s.pool.Query(ctx, `SELECT build_id, org_id, service_id, env_name, state, entrypoint, source_digest, profile, job, artifact_id, error, repository, assets, created_at, finished_at, claimed_by, claimed_at
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
			rows, err = s.pool.Query(ctx, `SELECT release_id, org_id, service_id, env_name, artifact_id, archive_digest, module_digest, config_revision, variables, secret_digest, created_at
				FROM krun_worker_releases WHERE release_id = ANY($1)`, releaseIDs)
			if err != nil {
				return state, err
			}
			if err = readReleaseRows(rows, &state); err != nil {
				rows.Close()
				return state, err
			}
			rows.Close()
			serviceIDs := []string{}
			for _, release := range state.Releases {
				serviceIDs = append(serviceIDs, release.Service)
			}
			if len(serviceIDs) > 0 {
				rows, err = s.pool.Query(ctx, `SELECT service_id, pool_id, org_id, network_profile, role, state, revision, created_at, updated_at
					FROM krun_worker_service_placements WHERE service_id = ANY($1)`, serviceIDs)
				if err != nil {
					return state, err
				}
				for rows.Next() {
					var placement WorkerPlacement
					if err = rows.Scan(&placement.Service, &placement.Pool, &placement.Organization, &placement.NetworkProfile,
						&placement.Role, &placement.State, &placement.Revision, &placement.Created, &placement.Updated); err != nil {
						rows.Close()
						return state, err
					}
					state.Placements[workerPlacementKey(placement.Service, placement.Pool)] = placement
				}
				if err = rows.Err(); err != nil {
					rows.Close()
					return state, err
				}
				rows.Close()
			}
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
		if err := rows.Scan(&r.ID, &r.Organization, &r.Service, &r.Environment, &r.Artifact, &r.ArchiveDigest, &r.ModuleDigest, &r.ConfigRevision, &variables, &r.SecretDigest, &r.Created); err != nil {
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
		if err := rows.Scan(&b.ID, &b.Organization, &b.Service, &b.Environment, &b.State, &b.Entrypoint, &b.SourceDigest, &b.Profile, &b.Job, &b.Artifact, &b.Error, &repository, &assets, &b.Created, &b.Finished, &b.ClaimedBy, &b.ClaimedAt); err != nil {
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
