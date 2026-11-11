package storage

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/krun-dev/krun/internal/domain"
)

const workerClaimLease = "60 seconds"

type OperationClaim struct {
	id         string
	replica    string
	generation int64
	StartedAt  time.Time
}

// ClaimOperation starts one validation attempt, including when the previous
// attempt belonged to the same replica. Generation zero predates this lease.
func (s *Store) ClaimOperation(ctx context.Context, id, replica string) (OperationClaim, bool, error) {
	claim := OperationClaim{id: id, replica: replica}
	err := s.pool.QueryRow(ctx, `UPDATE krun_worker_operations
		SET state='validating', claimed_by=$2, claimed_at=clock_timestamp(),
		    claim_generation=claim_generation+1, started_at=COALESCE(started_at,now()),
		    reconcile_after=now()+interval '2 seconds'
		WHERE operation_id=$1 AND reconcile_after<=now() AND (
			state='queued' OR (state='validating' AND (
				claim_generation=0 OR claimed_by=$2 OR claimed_at IS NULL
				OR claimed_at<=clock_timestamp()-$3::interval
			))
		)
		RETURNING claim_generation, started_at`, id, replica, workerClaimLease).Scan(&claim.generation, &claim.StartedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return OperationClaim{}, false, nil
	}
	return claim, err == nil, err
}

const activeOperationClaim = `operation_id=$1 AND state='validating' AND claimed_by=$2
	AND claim_generation=$3 AND claimed_at>clock_timestamp()-$4::interval`

func (s *Store) RenewOperationClaim(ctx context.Context, claim OperationClaim) (bool, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE krun_worker_operations SET claimed_at=clock_timestamp()
		WHERE `+activeOperationClaim, claim.id, claim.replica, claim.generation, workerClaimLease)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// commitOperation publishes a validated version. The environment row must still
// carry the revision the operation validated against, so a configuration edit
// that landed in between makes this a no-op instead of an overwrite.
func (s *Store) CommitOperation(ctx context.Context, claim OperationClaim) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)

	// Win this operation before changing the environment. The transient state
	// is visible only inside this transaction, so a crash rolls it back and a
	// concurrent committer cannot publish a version for an operation it lost.
	var worker, environment, version string
	var expectedRevision int
	err = tx.QueryRow(ctx, `UPDATE krun_worker_operations SET state='committing'
		WHERE `+activeOperationClaim+`
		RETURNING worker_id, env_name, version_id, expected_revision`,
		claim.id, claim.replica, claim.generation, workerClaimLease).Scan(&worker, &environment, &version, &expectedRevision)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var revision int
	err = tx.QueryRow(ctx, `UPDATE krun_worker_environments
		SET version_id=$4, deployment_revision=deployment_revision+1, updated_at=now()
		WHERE worker_id=$1 AND env_name=$2 AND deployment_revision=$3 AND enabled
		RETURNING deployment_revision`, worker, environment, expectedRevision, version).Scan(&revision)
	if errors.Is(err, pgx.ErrNoRows) {
		// The environment moved on: fail the operation, which is what the
		// previous single-writer version also did on a revision conflict.
		if _, err = tx.Exec(ctx, `UPDATE krun_worker_operations
			SET state='failed', error='Version or configuration revision conflict', finished_at=now()
			WHERE operation_id=$1 AND state='committing'`, claim.id); err != nil {
			return false, err
		}
		return false, tx.Commit(ctx)
	}
	if err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `UPDATE krun_worker_deployments SET committed_at=COALESCE(committed_at,now()) WHERE operation_id=$1`, claim.id); err != nil {
		return false, err
	}
	// Supersede an earlier committed operation only after this version has won
	// the environment CAS. A stale operation must not cancel live convergence.
	if _, err = tx.Exec(ctx, `UPDATE krun_worker_operations
		SET state='superseded', error='Replaced by '||$3, finished_at=now()
		WHERE worker_id=$1 AND env_name=$2 AND operation_id<>$3 AND state='committed'`,
		worker, environment, claim.id); err != nil {
		return false, err
	}
	// Steps 0-2 (artifact, candidate, commit) are finished by the time the
	// version is durable; step 3 stays running until acks converge.
	_, err = tx.Exec(ctx, `UPDATE krun_worker_operations
		SET state='committed', error='', deployment_revision=$2, edge_revision=0, edge_pool='', finished_at=NULL,
		    reconcile_after=now()+interval '2 seconds',
		    steps = (SELECT COALESCE(jsonb_agg(
		        CASE step->>'name'
		            WHEN 'artifact'    THEN jsonb_set(step, '{state}', '"done"')
		            WHEN 'candidate'   THEN jsonb_set(step, '{state}', '"done"')
		            WHEN 'commit'      THEN jsonb_set(step, '{state}', '"done"')
		            WHEN 'synchronize' THEN jsonb_set(step, '{state}', '"running"')
		            ELSE step END), steps)
		        FROM jsonb_array_elements(steps) AS step)
		WHERE operation_id=$1 AND state='committing'`, claim.id, revision)
	if err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

// confirmOperation records convergence once every serving Pod has acked the
// edge revision. It also appends the version history entry.
func (s *Store) ConfirmOperation(ctx context.Context, id, pool string, target domain.EdgeConfigCursor) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var epoch string
	if err := tx.QueryRow(ctx, `SELECT value FROM krun_meta WHERE key='worker_config_epoch' FOR SHARE`).Scan(&epoch); err != nil {
		return err
	}
	if epoch != target.Epoch {
		return nil
	}
	edgeRevision := target.Revision
	// Acquire the environment row before the committed operation row, matching
	// the order in which a replacement commit locks these two rows.
	tag, err := tx.Exec(ctx, `UPDATE krun_worker_environments e
		SET confirmed_version_id=o.version_id, updated_at=now()
		FROM krun_worker_operations o
		WHERE o.operation_id=$1 AND o.state='committed' AND o.edge_pool=$2 AND o.edge_revision=$3 AND o.edge_revision>0
			AND e.worker_id=o.worker_id AND e.env_name=o.env_name AND e.enabled
			AND e.deployment_revision=o.deployment_revision AND e.version_id=o.version_id
			AND `+activeOperationPool, id, pool, edgeRevision)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	// Hold the placement used for the ACK through commit. A concurrent move
	// can change the primary placement without writing the environment row.
	tag, err = tx.Exec(ctx, `SELECT 1 FROM krun_worker_operations o
		JOIN krun_worker_placements p ON p.worker_id=o.worker_id AND p.org_id=o.org_id
		WHERE o.operation_id=$1 AND p.pool_id=$2 AND p.role='primary' AND p.state='active'
		FOR SHARE OF p`, id, pool)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	tag, err = tx.Exec(ctx, `UPDATE krun_worker_operations
		SET state='available', error='', finished_at=now(),
		    steps = jsonb_set(steps, '{3,state}', '"done"')
		WHERE operation_id=$1 AND state='committed' AND edge_pool=$2 AND edge_revision=$3`, id, pool, edgeRevision)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO krun_worker_history (worker_id, env_name, revision, version_id, operation_id, kind, created_at)
		SELECT worker_id, env_name, deployment_revision, version_id, operation_id, kind, now()
		FROM krun_worker_operations WHERE operation_id=$1
		ON CONFLICT (worker_id, env_name, revision) DO NOTHING`, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

const activeOperationPool = `EXISTS (
	SELECT 1 FROM krun_worker_placements p
	WHERE p.worker_id=o.worker_id AND p.org_id=o.org_id AND p.pool_id=$2 AND p.role='primary' AND p.state='active'
)`

// PinOperationEdgeRevision reads the head and verifies the worker projection
// in the same database snapshot. A concurrent publisher can clear the dirty
// row, so a head obtained before publication is not evidence of convergence.
// Each serving pool needs its own target because ACKs use the global cursor.
func (s *Store) PinOperationEdgeRevision(ctx context.Context, id, pool string) (int64, error) {
	var stored int64
	err := s.pool.QueryRow(ctx, `UPDATE krun_worker_operations o SET edge_revision=m.value::bigint,edge_pool=$2
		FROM krun_meta m, krun_worker_environments e
		WHERE o.operation_id=$1 AND o.state='committed' AND (o.edge_revision=0 OR o.edge_pool<>$2)
			AND m.key='worker_config_head'
			AND e.worker_id=o.worker_id AND e.env_name=o.env_name AND e.enabled
			AND e.deployment_revision=o.deployment_revision AND e.version_id=o.version_id
			AND `+activeOperationPool+`
			AND NOT EXISTS (SELECT 1 FROM worker_config_dirty d WHERE d.worker_id=o.worker_id)
			AND EXISTS (
				SELECT 1 FROM worker_config_routes r
				JOIN worker_config_versions v ON v.version_id=r.version_id AND v.pool=r.pool
				WHERE r.worker_id=o.worker_id AND r.version_id=o.version_id AND r.pool=$2
					AND r.enabled AND r.expires_at IS NULL
			)
		RETURNING o.edge_revision`, id, pool).Scan(&stored)
	if !errors.Is(err, pgx.ErrNoRows) {
		return stored, err
	}
	err = s.pool.QueryRow(ctx, `SELECT o.edge_revision FROM krun_worker_operations o
		WHERE o.operation_id=$1 AND o.state='committed' AND o.edge_pool=$2
			AND `+activeOperationPool, id, pool).Scan(&stored)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return stored, nil
}

func (s *Store) ScheduleCommittedOperation(ctx context.Context, id string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE krun_worker_operations SET reconcile_after=now()+interval '2 seconds'
		WHERE operation_id=$1 AND state='committed' AND reconcile_after<=now()`, id)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (s *Store) NoteCommittedOperation(ctx context.Context, id string, edgeRevision int64, message string) error {
	_, err := s.pool.Exec(ctx, `UPDATE krun_worker_operations SET error=$2
		WHERE operation_id=$1 AND state='committed' AND edge_revision=$3`, id, message, edgeRevision)
	return err
}

func supersedeEnvironmentOperations(ctx context.Context, tx pgx.Tx, worker, environment, message string) error {
	_, err := tx.Exec(ctx, `UPDATE krun_worker_operations SET state='superseded',error=$3,finished_at=now()
		WHERE worker_id=$1 AND ($2='' OR env_name=$2) AND state='committed'`, worker, environment, message)
	return err
}

func (s *Store) NoteClaimedOperation(ctx context.Context, claim OperationClaim, message string) error {
	_, err := s.pool.Exec(ctx, `UPDATE krun_worker_operations SET error=$5
		WHERE `+activeOperationClaim, claim.id, claim.replica, claim.generation, workerClaimLease, message)
	return err
}

// failOperation marks an in-flight operation failed and stamps whichever step
// was running.
func (s *Store) FailClaimedOperation(ctx context.Context, claim OperationClaim, message string) error {
	_, err := s.pool.Exec(ctx, `UPDATE krun_worker_operations
		SET state='failed', error=$5, finished_at=now(),
		    steps = COALESCE((
		        SELECT jsonb_agg(CASE WHEN step->>'state' = 'running'
		            THEN jsonb_set(step, '{state}', '"failed"') || jsonb_build_object('detail', $5::text)
		            ELSE step END)
		        FROM jsonb_array_elements(steps) AS step
		    ), steps)
		WHERE `+activeOperationClaim, claim.id, claim.replica, claim.generation, workerClaimLease, message)
	return err
}
