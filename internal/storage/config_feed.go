package storage

import (
	"context"
	"time"

	"github.com/krun-dev/krun/internal/domain"
)

// ConfigRevisionsSince returns the contiguous batch after since and the oldest
// retained and latest revisions. Cursors outside that window must resync.
func (s *Store) ConfigRevisionsSince(ctx context.Context, since int64, limit int) ([]domain.WorkerConfigRevision, int64, int64, error) {
	if limit < 1 {
		limit = 1
	}
	if s.pool != nil {
		pool, _ := ctx.Value(WorkerRuntimeFeedPoolKey{}).(string)
		rows, err := s.pool.Query(ctx, `SELECT revision.config_revision, revision.digest,
			COALESCE((SELECT jsonb_agg((jsonb_build_object('type',op.op_type,'pool',op.pool)
                || CASE WHEN op.host<>'' THEN jsonb_build_object('host',op.host) ELSE '{}'::jsonb END
                || CASE op.op_type WHEN 'route_upsert' THEN jsonb_build_object('route',op.payload)
                   WHEN 'version_put' THEN jsonb_build_object('version',op.payload) ELSE '{}'::jsonb END
			) ORDER BY op.ordinal) FROM worker_config_ops op
				WHERE op.config_revision=revision.config_revision AND ($3='' OR op.pool=$3)), '[]'::jsonb),
			revision.created_at
			FROM worker_config_revisions revision
			WHERE revision.config_revision > $1 ORDER BY revision.config_revision LIMIT $2`, since, limit, pool)
		if err != nil {
			return nil, 0, 0, err
		}
		defer rows.Close()
		out := []domain.WorkerConfigRevision{}
		for rows.Next() {
			var item domain.WorkerConfigRevision
			var ops []byte
			if err := rows.Scan(&item.Revision, &item.Digest, &ops, &item.Created); err != nil {
				return nil, 0, 0, err
			}
			item.Ops = ops
			out = append(out, item)
		}
		if err := rows.Err(); err != nil {
			return nil, 0, 0, err
		}
		var floor int64
		var latest int64
		if err := s.pool.QueryRow(ctx, `SELECT COALESCE(MIN(config_revision),0), COALESCE(MAX(config_revision),0) FROM worker_config_revisions`).Scan(&floor, &latest); err != nil {
			return nil, 0, 0, err
		}
		return out, floor, latest, nil
	}
	s.configMu.Lock()
	defer s.configMu.Unlock()
	out := []domain.WorkerConfigRevision{}
	for _, item := range s.configRevs {
		if item.Revision > since && len(out) < limit {
			out = append(out, item)
		}
	}
	var floor int64
	var latest int64
	if len(s.configRevs) > 0 {
		floor = s.configRevs[0].Revision
		latest = s.configRevs[len(s.configRevs)-1].Revision
	}
	return out, floor, latest, nil
}

// ConfigState returns the latest and oldest retained revision for resync hints.
func (s *Store) ConfigState(ctx context.Context) (int64, int64, error) {
	if s.pool != nil {
		var latest, floor int64
		if err := s.pool.QueryRow(ctx, `SELECT COALESCE(MAX(config_revision),0), COALESCE(MIN(config_revision),0) FROM worker_config_revisions`).Scan(&latest, &floor); err != nil {
			return 0, 0, err
		}
		return latest, floor, nil
	}
	s.configMu.Lock()
	defer s.configMu.Unlock()
	if len(s.configRevs) == 0 {
		return 0, 0, nil
	}
	return s.configRevs[len(s.configRevs)-1].Revision, s.configRevs[0].Revision, nil
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
	if s.pool != nil {
		if _, err := s.pool.Exec(ctx, `INSERT INTO worker_runtime_acks (pod_uid, pool, revision, updated_at) VALUES ($1,$2,$3,$4)
			ON CONFLICT (pod_uid) DO UPDATE SET pool=excluded.pool,
				revision=GREATEST(worker_runtime_acks.revision, excluded.revision),
				updated_at=excluded.updated_at`, ack.UID, ack.Pool, ack.Revision, ack.Time); err != nil {
			return err
		}
		_, _ = s.pool.Exec(ctx, `DELETE FROM worker_runtime_acks WHERE updated_at < $1`, now().Add(-time.Minute))
		return nil
	}
	s.ackMu.Lock()
	defer s.ackMu.Unlock()
	if s.acks == nil {
		s.acks = map[string]domain.WorkerAck{}
	}
	if previous, ok := s.acks[ack.UID]; ok && previous.Revision > ack.Revision {
		ack.Revision = previous.Revision
	}
	s.acks[ack.UID] = ack
	for id, item := range s.acks {
		if now().Sub(item.Time) > time.Minute {
			delete(s.acks, id)
		}
	}
	return nil
}

// ReadAcks returns the current per-Pod revision map.
func (s *Store) ReadAcks(ctx context.Context) (map[string]domain.WorkerAck, error) {
	if s.pool != nil {
		rows, err := s.pool.Query(ctx, `SELECT pod_uid, pool, revision, updated_at FROM worker_runtime_acks`)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := map[string]domain.WorkerAck{}
		for rows.Next() {
			var ack domain.WorkerAck
			if err := rows.Scan(&ack.UID, &ack.Pool, &ack.Revision, &ack.Time); err != nil {
				return nil, err
			}
			out[ack.UID] = ack
		}
		return out, rows.Err()
	}
	s.ackMu.Lock()
	defer s.ackMu.Unlock()
	out := make(map[string]domain.WorkerAck, len(s.acks))
	for id, ack := range s.acks {
		out[id] = ack
	}
	return out, nil
}

type WorkerRuntimeFeedPoolKey struct{}
