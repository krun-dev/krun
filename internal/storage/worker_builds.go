package storage

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/krun-dev/krun/internal/domain"
)

var ErrArtifactBeingCollected = errors.New("artifact is being garbage-collected")

var ErrBuildClaimLost = errors.New("build claim is no longer owned")

// claimBuild moves a queued build to building, refreshes the current owner's
// lease, or takes over a lease whose owner stopped making progress. A
// controller calls this before every externally-visible build side effect.
func (s *Store) ClaimBuild(ctx context.Context, id, replica string) (bool, error) {
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
		SET state='building', claimed_by=$2, claimed_at=now(), started_at=COALESCE(started_at,now())
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

func (s *Store) RenewBuildClaim(ctx context.Context, id, replica string) (bool, error) {
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
func (s *Store) SetClaimedBuildState(ctx context.Context, id, replica, from, to string) (bool, error) {
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

func (s *Store) SetClaimedBuildRepository(ctx context.Context, id, replica string, source domain.WorkerBuildRepository) (bool, error) {
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
func (s *Store) StoreClaimedRepositoryBuild(ctx context.Context, id, replica string, build domain.WorkerBuild, private domain.WorkerBuildPrivate) error {
	if s.pool == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.memory == nil {
			return errors.New("store is not initialized")
		}
		current, ok := s.memory.Workers.Builds[id]
		if !ok || current.ClaimedBy != replica || workerBuildTerminal(current.State) {
			return ErrBuildClaimLost
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
	flags, err := json.Marshal(build.CompatibilityFlags)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE krun_worker_builds
		SET state=$3, entrypoint=$4, source_digest=$5, profile=$6,
		    compatibility_date=$7, compatibility_flags=$8, job=$9,
		    artifact_id=$10, error=$11, repository=$12, assets=$13, finished_at=$14
		WHERE build_id=$1 AND claimed_by=$2 AND state IN ('fetching','building')`,
		id, replica, build.State, build.Entrypoint, build.SourceDigest, build.Profile,
		build.CompatibilityDate, flags, build.Job, build.Artifact, build.Error, repository, assets, build.Finished)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrBuildClaimLost
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

// setBuildSource records the object-storage key once inline files have been
// packed into a bundle, and drops the inlined copy.
func (s *Store) SetBuildSource(ctx context.Context, id, key string) error {
	if s.pool == nil {
		return s.registerGeneratedBuildSource(ctx, id, "", key)
	}
	var organization string
	if err := s.pool.QueryRow(ctx, `SELECT org_id FROM krun_worker_builds WHERE build_id=$1`, id).Scan(&organization); err != nil {
		return err
	}
	return s.registerGeneratedBuildSource(ctx, id, organization, key)
}

// finishBuild records a terminal build state together with its log. Only one
// writer wins because the state transition is conditional.
func (s *Store) FinishBuild(ctx context.Context, id, state, message, artifact, log string) error {
	return s.finishBuildOwned(ctx, id, "", state, message, artifact, log)
}

func (s *Store) FinishClaimedBuild(ctx context.Context, id, replica, state, message, artifact, log string) error {
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
func (s *Store) ReserveBuildArtifact(ctx context.Context, id, replica string, artifact domain.WorkerArtifact) error {
	if s.pool == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.memory == nil {
			return errors.New("store is not initialized")
		}
		existing, ok := s.memory.Workers.Artifacts[artifact.ID]
		if ok && existing.State == "deleting" {
			return ErrArtifactBeingCollected
		}
		if ok && !sameArtifactContent(existing, artifact) {
			return ErrArtifactBeingCollected
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
	flags, err := json.Marshal(artifact.CompatibilityFlags)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO krun_worker_artifacts
		(artifact_id, org_id, state, profile, archive_digest, manifest_digest, module_digest, compatibility_date, compatibility_flags, size_bytes, object_key, storage_location, bucket, created_at)
		VALUES ($1,$2,'staging',$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		ON CONFLICT (artifact_id) DO NOTHING`, artifact.ID, artifact.Organization,
		artifact.Profile, artifact.ArchiveDigest, artifact.ManifestDigest, artifact.ModuleDigest,
		artifact.CompatibilityDate, flags, artifact.Size, artifact.ObjectKey, artifact.StorageLocation, artifact.Bucket, artifact.Created); err != nil {
		return err
	}
	var state string
	err = tx.QueryRow(ctx, `UPDATE krun_worker_artifacts SET use_revision=use_revision+1
		WHERE artifact_id=$1 AND state IN ('staging','finalized')
		  AND org_id=$2 AND profile=$3 AND archive_digest=$4 AND manifest_digest=$5 AND module_digest=$6
		  AND compatibility_date=$7 AND compatibility_flags=$8
		  AND size_bytes=$9 AND object_key=$10 AND storage_location=$11 AND bucket=$12
		RETURNING state`, artifact.ID, artifact.Organization, artifact.Profile, artifact.ArchiveDigest,
		artifact.ManifestDigest, artifact.ModuleDigest, artifact.CompatibilityDate, flags, artifact.Size, artifact.ObjectKey,
		artifact.StorageLocation, artifact.Bucket).Scan(&state)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrArtifactBeingCollected
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

func (s *Store) FinishClaimedBuildWithArtifact(ctx context.Context, id, replica, state, message string, artifact domain.WorkerArtifact, log string) error {
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
		if !ok || (stored.State != "staging" && stored.State != "finalized") || !sameArtifactContent(stored, artifact) {
			return ErrArtifactBeingCollected
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
	flags, err := json.Marshal(artifact.CompatibilityFlags)
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE krun_worker_artifacts
		SET state='finalized', gc_claimed_by='', gc_claimed_at=NULL
		WHERE artifact_id=$1 AND state IN ('staging','finalized')
		  AND org_id=$2 AND profile=$3 AND archive_digest=$4 AND manifest_digest=$5 AND module_digest=$6
		  AND compatibility_date=$7 AND compatibility_flags=$8
		  AND size_bytes=$9 AND object_key=$10 AND storage_location=$11 AND bucket=$12`,
		artifact.ID, artifact.Organization, artifact.Profile, artifact.ArchiveDigest, artifact.ManifestDigest,
		artifact.ModuleDigest, artifact.CompatibilityDate, flags, artifact.Size, artifact.ObjectKey, artifact.StorageLocation, artifact.Bucket)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrArtifactBeingCollected
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
