package storage

import (
	"context"
	"time"

	"github.com/krun-dev/krun/internal/domain"
)

func (s *Store) RetainExpired(ctx context.Context) error {
	if err := s.retainOrganizationInvitations(ctx); err != nil {
		return err
	}
	if s.pool == nil {
		s.mu.Lock()
		for id, a := range s.assetSessions {
			if !now().Before(a.Expires) {
				delete(s.assetSessions, id)
			}
		}
		s.mu.Unlock()
		return s.DeleteAuditsBefore(ctx, now().Add(-AuditRetention))
	}
	at := now()
	// Idle timeout and absolute expiry match the checks in authenticate.
	sessionCutoff := at.Add(-30 * time.Minute)
	if _, err := s.pool.Exec(ctx, `DELETE FROM krun_worker_deployments WHERE deployment_id IN(SELECT d.deployment_id FROM krun_worker_deployments d WHERE d.expires_at<=now() OR NOT EXISTS(SELECT 1 FROM krun_worker_environments e WHERE e.worker_id=d.worker_id AND e.env_name=d.env_name) ORDER BY d.expires_at LIMIT 1000)`); err != nil {
		return err
	}
	statements := []struct {
		sql  string
		args []any
	}{
		{`DELETE FROM krun_worker_asset_session_files WHERE NOT EXISTS(SELECT 1 FROM krun_worker_asset_sessions s WHERE s.session_id=krun_worker_asset_session_files.session_id AND s.expires_at > $1)`, []any{at}},
		{`DELETE FROM krun_worker_asset_sessions WHERE expires_at <= $1`, []any{at}},
		{`DELETE FROM krun_worker_scripts WHERE worker_id IN(SELECT s.worker_id FROM krun_worker_scripts s WHERE s.created_at < now()-interval '10 minutes' AND NOT EXISTS(SELECT 1 FROM krun_resources r WHERE r.resource_id=s.worker_id AND r.org_id=s.org_id AND r.kind='worker') LIMIT 1000)`, nil},
		{`DELETE FROM krun_oauth_authorizations WHERE expires_at <= $1`, []any{at}},
		{`DELETE FROM krun_oauth_refresh_tokens WHERE expires_at <= $1 OR NOT EXISTS(SELECT 1 FROM krun_oauth_grants g WHERE g.grant_id=krun_oauth_refresh_tokens.grant_id)`, []any{at}},
		{`DELETE FROM krun_oauth_grants WHERE expires_at <= $1`, []any{at}},
		{`DELETE FROM krun_rate_limits WHERE window_start < $1`, []any{at.Add(-rateLimitRetention)}},
		{`DELETE FROM krun_audits WHERE time < $1`, []any{at.Add(-AuditRetention)}},
		{`DELETE FROM krun_sessions WHERE absolute_at <= $1 OR (oauth_grant_id='' AND seen_at <= $2)`, []any{at, sessionCutoff}},
		{`DELETE FROM krun_browsers WHERE expires_at <= $1`, []any{at}},
		{`DELETE FROM krun_login_states WHERE expires_at <= $1`, []any{at}},
		{`DELETE FROM krun_email_logins WHERE expires_at <= $1`, []any{at}},
		{`DELETE FROM krun_email_send_locks WHERE touched_at < $1`, []any{at.Add(-24 * time.Hour)}},
		{`DELETE FROM krun_repository_connect_states WHERE expires_at <= $1`, []any{at}},
		{`DELETE FROM krun_invitations WHERE expires_at <= $1`, []any{at}},
		{`DELETE FROM krun_worker_idempotency WHERE created_at <= $1`, []any{at.Add(-workerIdempotencyRetain)}},
		{`DELETE FROM krun_worker_operations WHERE state IN ('succeeded','failed','available','superseded') AND created_at <= $1`, []any{at.Add(-workerOperationRetain)}},
		{`UPDATE krun_worker_build_private SET log_key='' WHERE build_id IN (SELECT p.build_id FROM krun_worker_build_private p JOIN krun_worker_builds b ON b.build_id=p.build_id WHERE b.created_at<=$1 AND length(p.log_key)>0 ORDER BY b.created_at LIMIT 1000)`, []any{at.Add(-workerBuildLogRetain)}},
		{`WITH victims AS MATERIALIZED (SELECT version_id FROM krun_worker_versions rel WHERE rel.created_at<=$1
 AND NOT EXISTS(SELECT 1 FROM krun_worker_environments e WHERE e.version_id=rel.version_id)
 AND NOT EXISTS(SELECT 1 FROM krun_worker_environments e WHERE e.confirmed_version_id=rel.version_id)
 AND NOT EXISTS(SELECT 1 FROM krun_worker_deployments d WHERE d.version_id=rel.version_id)
 AND NOT EXISTS(SELECT 1 FROM krun_worker_history h WHERE h.version_id=rel.version_id)
 AND NOT EXISTS(SELECT 1 FROM krun_worker_operations o WHERE o.version_id=rel.version_id)
 ORDER BY rel.created_at LIMIT 1000), bindings AS(DELETE FROM krun_worker_version_secrets WHERE version_id IN(SELECT version_id FROM victims)), r2 AS(DELETE FROM krun_worker_version_r2 WHERE version_id IN(SELECT version_id FROM victims))
 DELETE FROM krun_worker_versions WHERE version_id IN(SELECT version_id FROM victims)`, []any{at.Add(-WorkerArtifactRetain)}},
		{`DELETE FROM krun_worker_secret_versions WHERE secret_version_id IN (SELECT secret.secret_version_id FROM krun_worker_secret_versions secret WHERE secret.created_at<=$1
 AND NOT EXISTS(SELECT 1 FROM krun_worker_environment_secrets b WHERE b.secret_version_id=secret.secret_version_id)
 AND NOT EXISTS(SELECT 1 FROM krun_worker_version_secrets b WHERE b.secret_version_id=secret.secret_version_id) ORDER BY secret.created_at LIMIT 1000)`, []any{at.Add(-WorkerArtifactRetain)}},
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

// garbageArtifacts returns metadata whose version references have expired.
// The controller removes the S3 objects first and only then calls
// deleteGarbageArtifact, so a temporary object-store outage never loses the
// keys needed for a later cleanup attempt.
const workerArtifactGCLease = "10 minutes"

func (s *Store) GarbageArtifacts(ctx context.Context, cutoff time.Time, limit int) ([]domain.WorkerArtifact, error) {
	if s.pool == nil {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT artifact_id, org_id, state, archive_digest,
		manifest_digest, module_digest, size_bytes, object_key, storage_location, bucket, created_at, use_revision
		FROM krun_worker_artifacts a
		WHERE a.created_at <= $1 AND (
			a.state IN ('staging','finalized') OR (a.state='deleting' AND (a.gc_claimed_at IS NULL OR a.gc_claimed_at < now() - $3::interval))
		) AND NOT EXISTS (
			SELECT 1 FROM krun_worker_versions r WHERE r.artifact_id=a.artifact_id
		) AND NOT EXISTS (
			SELECT 1 FROM krun_worker_builds b WHERE b.artifact_id=a.artifact_id
		)
		ORDER BY a.created_at LIMIT $2`, cutoff, limit, workerArtifactGCLease)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.WorkerArtifact{}
	for rows.Next() {
		var a domain.WorkerArtifact
		if err := rows.Scan(&a.ID, &a.Organization, &a.State, &a.ArchiveDigest,
			&a.ManifestDigest, &a.ModuleDigest, &a.Size, &a.ObjectKey,
			&a.StorageLocation, &a.Bucket, &a.Created, &a.UseRevision); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) ClaimGarbageArtifact(ctx context.Context, id, replica string, expectedRevision int64) (bool, error) {
	if s.pool == nil {
		return false, nil
	}
	tag, err := s.pool.Exec(ctx, `UPDATE krun_worker_artifacts a
		SET state='deleting', gc_claimed_by=$2, gc_claimed_at=now()
		WHERE a.artifact_id=$1 AND (
			a.state IN ('staging','finalized') OR (a.state='deleting' AND (a.gc_claimed_at IS NULL OR a.gc_claimed_at < now() - $3::interval))
		)
		AND a.use_revision=$4
		AND NOT EXISTS (SELECT 1 FROM krun_worker_versions r WHERE r.artifact_id=a.artifact_id)
		AND NOT EXISTS (SELECT 1 FROM krun_worker_builds b WHERE b.artifact_id=a.artifact_id)`, id, replica, workerArtifactGCLease, expectedRevision)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (s *Store) DeleteGarbageArtifact(ctx context.Context, id, replica string) error {
	if s.pool == nil {
		return nil
	}
	_, err := s.pool.Exec(ctx, `DELETE FROM krun_worker_artifacts a
		WHERE a.artifact_id=$1 AND a.state='deleting' AND a.gc_claimed_by=$2 AND NOT EXISTS (
			SELECT 1 FROM krun_worker_versions r WHERE r.artifact_id=a.artifact_id
		) AND NOT EXISTS (
			SELECT 1 FROM krun_worker_builds b WHERE b.artifact_id=a.artifact_id
		)`, id, replica)
	return err
}

// blockGarbageArtifact turns an object owned by an unknown storage location
// into a durable, observable tombstone. Retrying with the current credentials
// can never succeed; an operator must clean the old store and then remove (or
// deliberately re-home) this metadata row.
func (s *Store) BlockGarbageArtifact(ctx context.Context, id, replica string, expectedRevision int64) (bool, error) {
	if s.pool == nil {
		return false, nil
	}
	tag, err := s.pool.Exec(ctx, `UPDATE krun_worker_artifacts a
		SET state='cleanup_blocked', gc_claimed_by=$2, gc_claimed_at=now()
		WHERE artifact_id=$1 AND use_revision=$3 AND state IN ('staging','finalized','deleting')
		AND NOT EXISTS (SELECT 1 FROM krun_worker_versions r WHERE r.artifact_id=a.artifact_id)
		AND NOT EXISTS (SELECT 1 FROM krun_worker_builds b WHERE b.artifact_id=a.artifact_id)`, id, replica, expectedRevision)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// RunRetention bounds each cleanup cycle independently; callers choose logging.
func (s *Store) RunRetention(ctx context.Context, interval time.Duration, onError func(error)) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cycle, cancel := context.WithTimeout(ctx, 20*time.Second)
			err := s.RetainExpired(cycle)
			cancel()
			if err != nil && onError != nil {
				onError(err)
			}
		}
	}
}
