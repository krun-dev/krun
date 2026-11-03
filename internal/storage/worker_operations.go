package storage

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/krun-dev/krun/internal/domain"
)

const workerClaimLease = "60 seconds"

// claimOperation prepares a queued operation for validation.
func (s *Store) ClaimOperation(ctx context.Context, id, replica string) (bool, error) {
	if s.pool == nil {
		return true, nil
	}
	tag, err := s.pool.Exec(ctx, `UPDATE krun_worker_operations
		SET state='validating', claimed_by=$2, claimed_at=now(), started_at=COALESCE(started_at,now())
		WHERE operation_id=$1 AND state='queued'`, id, replica)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// claimStaleOperation refreshes the current owner's validating lease or takes
// over one whose claimer died.
func (s *Store) ClaimStaleOperation(ctx context.Context, id, replica string) (bool, error) {
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

// commitOperation publishes a validated version. The environment row must still
// carry the revision the operation validated against, so a configuration edit
// that landed in between makes this a no-op instead of an overwrite.
func (s *Store) CommitOperation(ctx context.Context, op domain.WorkerOperation) (bool, error) {
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
	// concurrent committer cannot publish a version for an operation it lost.
	tag, err := tx.Exec(ctx, `UPDATE krun_worker_operations SET state='committing'
		WHERE operation_id=$1 AND state IN ('queued','validating')`, op.ID)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 0 {
		return false, tx.Commit(ctx)
	}
	tag, err = tx.Exec(ctx, `UPDATE krun_worker_environments
		SET version_id=$4, deployment_revision=deployment_revision+1, updated_at=now()
		WHERE service_id=$1 AND env_name=$2 AND deployment_revision=$3 AND enabled`,
		op.Service, op.Environment, op.ExpectedRevision, op.Version)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 0 {
		// The environment moved on: fail the operation, which is what the
		// previous single-writer version also did on a revision conflict.
		if _, err = tx.Exec(ctx, `UPDATE krun_worker_operations
			SET state='failed', error='Version or configuration revision conflict', finished_at=now()
			WHERE operation_id=$1 AND state='committing'`, op.ID); err != nil {
			return false, err
		}
		return false, tx.Commit(ctx)
	}
	if _, err = tx.Exec(ctx, `UPDATE krun_worker_deployments SET committed_at=COALESCE(committed_at,now()) WHERE operation_id=$1`, op.ID); err != nil {
		return false, err
	}
	// Supersede an earlier committed operation only after this version has won
	// the environment CAS. A stale operation must not cancel live convergence.
	if _, err = tx.Exec(ctx, `UPDATE krun_worker_operations
		SET state='superseded', error='Replaced by '||$3, finished_at=now()
		WHERE service_id=$1 AND env_name=$2 AND operation_id<>$3 AND state='committed'`,
		op.Service, op.Environment, op.ID); err != nil {
		return false, err
	}
	var revision int
	if err = tx.QueryRow(ctx, `SELECT deployment_revision FROM krun_worker_environments WHERE service_id=$1 AND env_name=$2`, op.Service, op.Environment).Scan(&revision); err != nil {
		return false, err
	}
	// Steps 0-2 (artifact, candidate, commit) are finished by the time the
	// version is durable; step 3 stays running until acks converge.
	tag, err = tx.Exec(ctx, `UPDATE krun_worker_operations
		SET state='committed', error='', deployment_revision=$2, edge_revision=0, finished_at=NULL,
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
// edge revision. It also appends the version history entry.
func (s *Store) ConfirmOperation(ctx context.Context, op domain.WorkerOperation) error {
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
	if _, err = tx.Exec(ctx, `INSERT INTO krun_worker_history (service_id, env_name, revision, version_id, operation_id, kind, created_at)
		SELECT service_id, env_name, deployment_revision, version_id, operation_id, kind, now()
		FROM krun_worker_operations WHERE operation_id=$1
		ON CONFLICT (service_id, env_name, revision) DO NOTHING`, op.ID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE krun_worker_environments
		SET confirmed_version_id = (SELECT version_id FROM krun_worker_operations WHERE operation_id=$1), updated_at=now()
		WHERE service_id=$2 AND env_name=$3 AND version_id=(SELECT version_id FROM krun_worker_operations WHERE operation_id=$1)`,
		op.ID, op.Service, op.Environment); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// setOperationEdgeRevision records the first published config revision that
// contains this operation's commit. That revision is a durable lower bound for
// convergence: later unrelated publishes must not move an older operation's
// acknowledgement target forward forever.
func (s *Store) SetOperationEdgeRevision(ctx context.Context, id string, revision int64) (int64, error) {
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

func (s *Store) NoteOperationMessage(ctx context.Context, id, message string) error {
	if s.pool == nil {
		return nil
	}
	_, err := s.pool.Exec(ctx, `UPDATE krun_worker_operations SET error=$2
		WHERE operation_id=$1 AND state NOT IN ('succeeded','failed','available','superseded')`, id, message)
	return err
}

// failOperation marks an in-flight operation failed and stamps whichever step
// was running, matching what the in-memory helper used to do.
func (s *Store) FailOperation(ctx context.Context, id, message string) error {
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
