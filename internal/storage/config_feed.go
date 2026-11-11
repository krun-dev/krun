package storage

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/krun-dev/krun/internal/domain"
)

// ConfigRevisionsSince returns the contiguous batch after since and the oldest
// retained and latest revisions. Cursors outside that window must resync.
func (s *Store) ConfigRevisionsSince(ctx context.Context, since int64, limit int) ([]domain.EdgeConfigRevision, int64, domain.EdgeConfigCursor, error) {
	var head domain.EdgeConfigCursor
	if limit < 1 {
		limit = 1
	}
	pool, _ := ctx.Value(WorkerRuntimeFeedPoolKey{}).(string)
	rows, err := s.pool.Query(ctx, `SELECT config.epoch,config.head,
			COALESCE((SELECT MIN(config_revision) FROM worker_config_revisions),0),
			revision.config_revision, revision.digest,
			COALESCE((SELECT jsonb_agg((jsonb_build_object('type',op.op_type,'pool',op.pool)
                || CASE WHEN op.host<>'' THEN jsonb_build_object('host',op.host) ELSE '{}'::jsonb END
                || CASE op.op_type WHEN 'route_upsert' THEN jsonb_build_object('route',op.payload)
                   WHEN 'version_put' THEN jsonb_build_object('version',op.payload) ELSE '{}'::jsonb END
			) ORDER BY op.ordinal) FROM worker_config_ops op
				WHERE op.config_revision=revision.config_revision AND ($3='' OR op.pool=$3)), '[]'::jsonb),
			revision.created_at
			FROM (`+readConfigPosition+`) AS config(epoch,head)
			LEFT JOIN LATERAL (
				SELECT config_revision,digest,created_at FROM worker_config_revisions
				WHERE config_revision>$1 ORDER BY config_revision LIMIT $2
			) revision ON true
			ORDER BY revision.config_revision`, since, limit, pool)
	if err != nil {
		return nil, 0, head, err
	}
	defer rows.Close()
	out := []domain.EdgeConfigRevision{}
	var floor int64
	for rows.Next() {
		var revision *int64
		var digest *string
		var created *time.Time
		var ops []byte
		if err := rows.Scan(&head.Epoch, &head.Revision, &floor, &revision, &digest, &ops, &created); err != nil {
			return nil, 0, head, err
		}
		if revision != nil {
			out = append(out, domain.EdgeConfigRevision{Revision: *revision, Digest: *digest, Ops: ops, Created: *created})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, 0, head, err
	}
	if head.Epoch == "" {
		return nil, 0, head, pgx.ErrNoRows
	}
	return out, floor, head, nil
}

const readConfigPosition = `SELECT e.value,h.value::bigint
	FROM krun_meta e, krun_meta h WHERE e.key='worker_config_epoch' AND h.key='worker_config_head'`

func (s *Store) ConfigPosition(ctx context.Context) (domain.EdgeConfigCursor, error) {
	var position domain.EdgeConfigCursor
	err := s.pool.QueryRow(ctx, readConfigPosition).Scan(&position.Epoch, &position.Revision)
	return position, err
}

// ConfigState returns the latest and oldest retained revision for resync hints.
func (s *Store) ConfigState(ctx context.Context) (int64, int64, error) {
	var latest, floor int64
	if err := s.pool.QueryRow(ctx, `SELECT COALESCE(MAX(config_revision),0), COALESCE(MIN(config_revision),0) FROM worker_config_revisions`).Scan(&latest, &floor); err != nil {
		return 0, 0, err
	}
	return latest, floor, nil
}

// ConfigNotify returns the current in-process broadcast channel. Callers read
// the channel before checking durable state to avoid missing a signal.
func (s *Store) ConfigNotify() <-chan struct{} {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	if s.configCh == nil {
		s.configCh = make(chan struct{})
	}
	return s.configCh
}

// SignalConfig wakes local long-polls after a revision is committed.
func (s *Store) SignalConfig() {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	if s.configCh != nil {
		close(s.configCh)
	}
	s.configCh = make(chan struct{})
}

// RecordAck stores a runtime heartbeat. Acks are per Pod and deliberately do
// not live in the IAM snapshot.
func (s *Store) RecordAck(ctx context.Context, ack domain.WorkerAck) error {
	if ack.Time.IsZero() {
		ack.Time = now()
	}
	var epoch string
	var accepted bool
	err := s.pool.QueryRow(ctx, `WITH config(epoch,head) AS MATERIALIZED (`+readConfigPosition+` FOR SHARE OF e),
		accepted AS (
			INSERT INTO worker_runtime_acks(pod_uid,pool,revision,updated_at,config_epoch)
			SELECT $1,$2,$3::bigint,$4,epoch FROM config WHERE epoch=$5 AND $3::bigint BETWEEN 0 AND head
			ON CONFLICT (pod_uid,config_epoch) DO UPDATE SET pool=excluded.pool,
				revision=GREATEST(worker_runtime_acks.revision,excluded.revision),updated_at=excluded.updated_at
			RETURNING 1
		)
		SELECT epoch,EXISTS(SELECT 1 FROM accepted) FROM config`,
		ack.UID, ack.Pool, ack.Revision, ack.Time, ack.Epoch).Scan(&epoch, &accepted)
	if err != nil {
		return err
	}
	if epoch != ack.Epoch {
		return domain.Conflict("CONFIG_EPOCH_CHANGED", "Runtime configuration epoch changed")
	}
	if !accepted {
		return domain.Invalid("ACK_INVALID", "Config revision is outside the published range")
	}
	_, _ = s.pool.Exec(ctx, `DELETE FROM worker_runtime_acks WHERE updated_at < $1`, now().Add(-time.Minute))
	return nil
}

// ReadAcks returns the current per-Pod revision map.
func (s *Store) ReadAcks(ctx context.Context) (map[string]domain.WorkerAck, error) {
	rows, err := s.pool.Query(ctx, `SELECT a.pod_uid,a.pool,a.revision,a.updated_at,a.config_epoch
		FROM worker_runtime_acks a JOIN krun_meta m ON m.key='worker_config_epoch' AND m.value=a.config_epoch`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]domain.WorkerAck{}
	for rows.Next() {
		var ack domain.WorkerAck
		if err := rows.Scan(&ack.UID, &ack.Pool, &ack.Revision, &ack.Time, &ack.Epoch); err != nil {
			return nil, err
		}
		out[ack.UID] = ack
	}
	return out, rows.Err()
}

type WorkerRuntimeFeedPoolKey struct{}
