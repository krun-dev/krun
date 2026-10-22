package control

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// AppendConfigRevision persists one immutable edge-config revision and returns
// the assigned revision plus the oldest retained revision (ops_floor).
func (s *Store) AppendConfigRevision(ctx context.Context, digest string, payload, ops []byte, retain int) (int64, int64, error) {
	if retain < 1 {
		retain = 1
	}
	if s.pool != nil {
		var revision int64
		if err := s.pool.QueryRow(ctx, `INSERT INTO worker_config_revisions (digest, payload, ops) VALUES ($1,$2,$3) RETURNING config_revision`, digest, payload, ops).Scan(&revision); err != nil {
			return 0, 0, err
		}
		floor := revision - int64(retain) + 1
		if floor < 1 {
			floor = 1
		}
		if _, err := s.pool.Exec(ctx, `DELETE FROM worker_config_revisions WHERE config_revision < $1`, floor); err != nil {
			return revision, floor, err
		}
		return revision, floor, nil
	}
	s.configMu.Lock()
	defer s.configMu.Unlock()
	s.configSeq++
	revision := s.configSeq
	s.configRevs = append(s.configRevs, workerConfigRevision{Revision: revision, Digest: digest, Payload: payload, Ops: ops, Created: now()})
	floor := revision - int64(retain) + 1
	if floor < 1 {
		floor = 1
	}
	cut := 0
	for cut < len(s.configRevs) && s.configRevs[cut].Revision < floor {
		cut++
	}
	if cut > 0 {
		s.configRevs = append([]workerConfigRevision(nil), s.configRevs[cut:]...)
	}
	return revision, floor, nil
}

// LatestConfigRevision returns the most recent projection, if any.
func (s *Store) LatestConfigRevision(ctx context.Context) (workerConfigRevision, bool, error) {
	if s.pool != nil {
		var out workerConfigRevision
		var payload, ops []byte
		err := s.pool.QueryRow(ctx, `SELECT config_revision, digest, payload, ops, created_at FROM worker_config_revisions ORDER BY config_revision DESC LIMIT 1`).Scan(&out.Revision, &out.Digest, &payload, &ops, &out.Created)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return workerConfigRevision{}, false, nil
			}
			return workerConfigRevision{}, false, err
		}
		out.Payload, out.Ops = payload, ops
		return out, true, nil
	}
	s.configMu.Lock()
	defer s.configMu.Unlock()
	if len(s.configRevs) == 0 {
		return workerConfigRevision{}, false, nil
	}
	last := s.configRevs[len(s.configRevs)-1]
	return last, true, nil
}

// ConfigRevisionsSince returns the contiguous batch after since and the oldest
// retained revision. A caller whose since is below the floor must resync.
func (s *Store) ConfigRevisionsSince(ctx context.Context, since int64, limit int) ([]workerConfigRevision, int64, error) {
	if limit < 1 {
		limit = 1
	}
	if s.pool != nil {
		rows, err := s.pool.Query(ctx, `SELECT config_revision, digest, payload, ops, created_at FROM worker_config_revisions WHERE config_revision > $1 ORDER BY config_revision LIMIT $2`, since, limit)
		if err != nil {
			return nil, 0, err
		}
		defer rows.Close()
		out := []workerConfigRevision{}
		for rows.Next() {
			var item workerConfigRevision
			var payload, ops []byte
			if err := rows.Scan(&item.Revision, &item.Digest, &payload, &ops, &item.Created); err != nil {
				return nil, 0, err
			}
			item.Payload, item.Ops = payload, ops
			out = append(out, item)
		}
		if err := rows.Err(); err != nil {
			return nil, 0, err
		}
		var floor int64
		if err := s.pool.QueryRow(ctx, `SELECT COALESCE(MIN(config_revision),0) FROM worker_config_revisions`).Scan(&floor); err != nil {
			return nil, 0, err
		}
		return out, floor, nil
	}
	s.configMu.Lock()
	defer s.configMu.Unlock()
	out := []workerConfigRevision{}
	for _, item := range s.configRevs {
		if item.Revision > since && len(out) < limit {
			out = append(out, item)
		}
	}
	var floor int64
	if len(s.configRevs) > 0 {
		floor = s.configRevs[0].Revision
	}
	return out, floor, nil
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
func (s *Store) RecordAck(ctx context.Context, ack WorkerAck) error {
	if ack.Time.IsZero() {
		ack.Time = now()
	}
	if s.pool != nil {
		if _, err := s.pool.Exec(ctx, `INSERT INTO worker_runtime_acks (pod_uid, pool, revision, updated_at) VALUES ($1,$2,$3,$4) ON CONFLICT (pod_uid) DO UPDATE SET pool=excluded.pool, revision=excluded.revision, updated_at=excluded.updated_at`, ack.UID, ack.Pool, ack.Revision, ack.Time); err != nil {
			return err
		}
		_, _ = s.pool.Exec(ctx, `DELETE FROM worker_runtime_acks WHERE updated_at < $1`, now().Add(-time.Minute))
		return nil
	}
	s.ackMu.Lock()
	defer s.ackMu.Unlock()
	if s.acks == nil {
		s.acks = map[string]WorkerAck{}
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
func (s *Store) ReadAcks(ctx context.Context) (map[string]WorkerAck, error) {
	if s.pool != nil {
		rows, err := s.pool.Query(ctx, `SELECT pod_uid, pool, revision, updated_at FROM worker_runtime_acks`)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := map[string]WorkerAck{}
		for rows.Next() {
			var ack WorkerAck
			if err := rows.Scan(&ack.UID, &ack.Pool, &ack.Revision, &ack.Time); err != nil {
				return nil, err
			}
			out[ack.UID] = ack
		}
		return out, rows.Err()
	}
	s.ackMu.Lock()
	defer s.ackMu.Unlock()
	out := make(map[string]WorkerAck, len(s.acks))
	for id, ack := range s.acks {
		out[id] = ack
	}
	return out, nil
}
