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
			if err := s.retainExpired(ctx); err != nil && ctx.Err() == nil {
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
		{`DELETE FROM krun_audits WHERE time < $1`, []any{at.Add(-auditRetention)}},
		{`DELETE FROM krun_sessions WHERE absolute_at <= $1 OR seen_at <= $2`, []any{at, sessionCutoff}},
		{`DELETE FROM krun_browsers WHERE expires_at <= $1`, []any{at}},
		{`DELETE FROM krun_login_states WHERE expires_at <= $1`, []any{at}},
		{`DELETE FROM krun_email_logins WHERE expires_at <= $1`, []any{at}},
		{`DELETE FROM krun_device_codes WHERE expires_at <= $1`, []any{at}},
		{`DELETE FROM krun_repository_connect_states WHERE expires_at <= $1`, []any{at}},
		{`DELETE FROM krun_invitations WHERE expires_at <= $1`, []any{at}},
		{`DELETE FROM krun_worker_idempotency WHERE created_at <= $1`, []any{at.Add(-workerIdempotencyRetain)}},
		{`DELETE FROM krun_worker_operations WHERE state IN ('succeeded','failed','available','superseded') AND created_at <= $1`, []any{at.Add(-workerOperationRetain)}},
		{`DELETE FROM krun_worker_history WHERE ctid IN (
			SELECT ctid FROM (
				SELECT ctid, row_number() OVER (PARTITION BY service_id, env_name ORDER BY created_at DESC) AS position
				FROM krun_worker_history
			) ranked WHERE position > $1
		)`, []any{workerHistoryRetainPerEnv}},
		// Build logs are the largest single column; keep them long enough to
		// debug a failed build, then drop the text and keep the metadata.
		{`UPDATE krun_worker_build_private SET log = '' WHERE length(log) > 0 AND build_id IN (SELECT build_id FROM krun_worker_builds WHERE created_at <= $1)`, []any{at.Add(-workerBuildLogRetain)}},
		// Keep the newest terminal builds per service; the private row (which
		// carries the log) is dropped first so a prune cannot strand a build
		// without its inputs.
		{`DELETE FROM krun_worker_build_private WHERE build_id IN (
			SELECT build_id FROM (
				SELECT b.build_id, row_number() OVER (PARTITION BY b.service_id ORDER BY b.created_at DESC) AS position
				FROM krun_worker_builds b
				WHERE b.state IN ('succeeded','failed')
			) ranked WHERE position > $1
		)`, []any{workerBuildRetainPerService}},
		{`DELETE FROM krun_worker_builds WHERE build_id IN (
			SELECT build_id FROM (
				SELECT b.build_id, row_number() OVER (PARTITION BY b.service_id ORDER BY b.created_at DESC) AS position
				FROM krun_worker_builds b
				WHERE b.state IN ('succeeded','failed')
			) ranked WHERE position > $1
		)`, []any{workerBuildRetainPerService}},
		// Orphan artifacts: unreferenced by any release and older than the window.
		{`DELETE FROM krun_worker_artifacts WHERE created_at <= $1 AND artifact_id NOT IN (SELECT artifact_id FROM krun_worker_releases)`, []any{at.Add(-workerArtifactRetain)}},
	}
	for _, statement := range statements {
		if _, err := s.pool.Exec(ctx, statement.sql, statement.args...); err != nil {
			return err
		}
	}
	return nil
}
