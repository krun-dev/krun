package control

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/krun-dev/krun/internal/localize"
)

// retain removes rows that grow without bound. Every per-request, per-build or
// per-release table has a limit here; see AGENTS.md section 9.5.
//
// This replaces the old per-request cleanup(st) scan: expired sessions and
// login transactions are now deleted by a single indexed DELETE on a timer
// instead of being walked on every authenticated request.
func (s *Store) retain(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cycle, cancel := context.WithTimeout(ctx, 20*time.Second)
			err := s.retainExpired(cycle)
			cancel()
			if err != nil && ctx.Err() == nil {
				fmt.Fprintln(os.Stderr, localize.Text(localize.Language(ctx), "krun retention cleanup failed:"), localize.Error(localize.Language(ctx), err))
			}
		}
	}
}

func (s *Store) retainExpired(ctx context.Context) error {
	if s.pool == nil {
		return nil
	}
	at := now()
	// Idle timeout and absolute expiry match the checks in authenticate.
	sessionCutoff := at.Add(-30 * time.Minute)
	statements := []struct {
		sql  string
		args []any
	}{
		{`DELETE FROM krun_rate_limits WHERE window_start < $1`, []any{at.Add(-rateLimitRetention)}},
		{`DELETE FROM krun_audits WHERE time < $1`, []any{at.Add(-auditRetention)}},
		{`DELETE FROM krun_sessions WHERE absolute_at <= $1 OR seen_at <= $2`, []any{at, sessionCutoff}},
		{`DELETE FROM krun_browsers WHERE expires_at <= $1`, []any{at}},
		{`DELETE FROM krun_login_states WHERE expires_at <= $1`, []any{at}},
		{`DELETE FROM krun_email_logins WHERE expires_at <= $1`, []any{at}},
		{`DELETE FROM krun_email_send_locks WHERE touched_at < $1`, []any{at.Add(-24 * time.Hour)}},
		{`DELETE FROM krun_device_codes WHERE expires_at <= $1`, []any{at}},
		{`DELETE FROM krun_repository_connect_states WHERE expires_at <= $1`, []any{at}},
		{`DELETE FROM krun_invitations WHERE expires_at <= $1`, []any{at}},
		{`DELETE FROM krun_worker_idempotency WHERE created_at <= $1`, []any{at.Add(-workerIdempotencyRetain)}},
		{`DELETE FROM krun_worker_operations WHERE state IN ('succeeded','failed','available','superseded') AND created_at <= $1`, []any{at.Add(-workerOperationRetain)}},
		{`UPDATE krun_worker_build_private SET log='' WHERE build_id IN (SELECT p.build_id FROM krun_worker_build_private p JOIN krun_worker_builds b ON b.build_id=p.build_id WHERE b.created_at<=$1 AND length(p.log)>0 ORDER BY b.created_at LIMIT 1000)`, []any{at.Add(-workerBuildLogRetain)}},
		{`WITH victims AS MATERIALIZED (SELECT release_id FROM krun_worker_releases rel WHERE rel.created_at<=$1
 AND NOT EXISTS(SELECT 1 FROM krun_worker_environments e WHERE e.release_id=rel.release_id)
 AND NOT EXISTS(SELECT 1 FROM krun_worker_environments e WHERE e.confirmed_release_id=rel.release_id)
 AND NOT EXISTS(SELECT 1 FROM krun_worker_history h WHERE h.release_id=rel.release_id)
 AND NOT EXISTS(SELECT 1 FROM krun_worker_operations o WHERE o.release_id=rel.release_id)
 ORDER BY rel.created_at LIMIT 1000), bindings AS(DELETE FROM krun_worker_release_secrets WHERE release_id IN(SELECT release_id FROM victims))
 DELETE FROM krun_worker_releases WHERE release_id IN(SELECT release_id FROM victims)`, []any{at.Add(-workerArtifactRetain)}},
		{`DELETE FROM krun_worker_secret_versions WHERE secret_version_id IN (SELECT secret.secret_version_id FROM krun_worker_secret_versions secret WHERE secret.created_at<=$1
 AND NOT EXISTS(SELECT 1 FROM krun_worker_environment_secrets b WHERE b.secret_version_id=secret.secret_version_id)
 AND NOT EXISTS(SELECT 1 FROM krun_worker_release_secrets b WHERE b.secret_version_id=secret.secret_version_id) ORDER BY secret.created_at LIMIT 1000)`, []any{at.Add(-workerArtifactRetain)}},
	}
	for _, statement := range statements {
		if _, err := s.pool.Exec(ctx, boundedRetentionDelete(statement.sql), statement.args...); err != nil {
			return err
		}
	}
	if err := s.retainWorkerScopes(ctx); err != nil {
		return err
	}
	return s.retainOrphanInputs(ctx)
}

// garbageArtifacts returns metadata whose release references have expired.
// The controller removes the S3 objects first and only then calls
// deleteGarbageArtifact, so a temporary object-store outage never loses the
// keys needed for a later cleanup attempt.
const workerArtifactGCLease = "10 minutes"

func (s *Store) garbageArtifacts(ctx context.Context, cutoff time.Time, limit int) ([]WorkerArtifact, error) {
	if s.pool == nil {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT artifact_id, org_id, state, archive_digest,
		manifest_digest, module_digest, size_bytes, object_key, storage_location, bucket, created_at, use_revision
		FROM krun_worker_artifacts a
		WHERE a.created_at <= $1 AND (
			a.state IN ('staging','finalized') OR (a.state='deleting' AND (a.gc_claimed_at IS NULL OR a.gc_claimed_at < now() - $3::interval))
		) AND NOT EXISTS (
			SELECT 1 FROM krun_worker_releases r WHERE r.artifact_id=a.artifact_id
		) AND NOT EXISTS (
			SELECT 1 FROM krun_worker_builds b WHERE b.artifact_id=a.artifact_id
		)
		ORDER BY a.created_at LIMIT $2`, cutoff, limit, workerArtifactGCLease)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []WorkerArtifact{}
	for rows.Next() {
		var a WorkerArtifact
		if err := rows.Scan(&a.ID, &a.Organization, &a.State, &a.ArchiveDigest,
			&a.ManifestDigest, &a.ModuleDigest, &a.Size, &a.ObjectKey,
			&a.StorageLocation, &a.Bucket, &a.Created, &a.UseRevision); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) claimGarbageArtifact(ctx context.Context, id, replica string, expectedRevision int64) (bool, error) {
	if s.pool == nil {
		return false, nil
	}
	tag, err := s.pool.Exec(ctx, `UPDATE krun_worker_artifacts a
		SET state='deleting', gc_claimed_by=$2, gc_claimed_at=now()
		WHERE a.artifact_id=$1 AND (
			a.state IN ('staging','finalized') OR (a.state='deleting' AND (a.gc_claimed_at IS NULL OR a.gc_claimed_at < now() - $3::interval))
		)
		AND a.use_revision=$4
		AND NOT EXISTS (SELECT 1 FROM krun_worker_releases r WHERE r.artifact_id=a.artifact_id)
		AND NOT EXISTS (SELECT 1 FROM krun_worker_builds b WHERE b.artifact_id=a.artifact_id)`, id, replica, workerArtifactGCLease, expectedRevision)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (s *Store) deleteGarbageArtifact(ctx context.Context, id, replica string) error {
	if s.pool == nil {
		return nil
	}
	_, err := s.pool.Exec(ctx, `DELETE FROM krun_worker_artifacts a
		WHERE a.artifact_id=$1 AND a.state='deleting' AND a.gc_claimed_by=$2 AND NOT EXISTS (
			SELECT 1 FROM krun_worker_releases r WHERE r.artifact_id=a.artifact_id
		) AND NOT EXISTS (
			SELECT 1 FROM krun_worker_builds b WHERE b.artifact_id=a.artifact_id
		)`, id, replica)
	return err
}

// blockGarbageArtifact turns an object owned by an unknown storage location
// into a durable, observable tombstone. Retrying with the current credentials
// can never succeed; an operator must clean the old store and then remove (or
// deliberately re-home) this metadata row.
func (s *Store) blockGarbageArtifact(ctx context.Context, id, replica string, expectedRevision int64) (bool, error) {
	if s.pool == nil {
		return false, nil
	}
	tag, err := s.pool.Exec(ctx, `UPDATE krun_worker_artifacts a
		SET state='cleanup_blocked', gc_claimed_by=$2, gc_claimed_at=now()
		WHERE artifact_id=$1 AND use_revision=$3 AND state IN ('staging','finalized','deleting')
		AND NOT EXISTS (SELECT 1 FROM krun_worker_releases r WHERE r.artifact_id=a.artifact_id)
		AND NOT EXISTS (SELECT 1 FROM krun_worker_builds b WHERE b.artifact_id=a.artifact_id)`, id, replica, expectedRevision)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// retainedWorkerSourceKeys is the object-side reachability set. Source bundles
// stay available exactly while their retained build-private row exists;
// abandoned presigned uploads have no row and remain eligible for age-based GC.
func (s *Store) retainedWorkerSourceKeys(ctx context.Context) (map[string]struct{}, error) {
	out := map[string]struct{}{}
	if s.pool == nil {
		return out, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT source_key FROM krun_worker_build_private WHERE source_key <> ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		out[key] = struct{}{}
	}
	return out, rows.Err()
}
