package control

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// AppendConfigRevision persists one immutable edge-config revision and returns
// the assigned revision plus the oldest retained revision (ops_floor).
func (s *Store) AppendConfigRevision(ctx context.Context, digest string, payload, ops []byte, retain int) (int64, int64, error) {
	latest, _, err := s.ConfigState(ctx)
	if err != nil {
		return 0, 0, err
	}
	for {
		revision, floor, appended, err := s.AppendConfigRevisionCAS(ctx, latest, digest, payload, ops, retain)
		if err != nil || appended {
			return revision, floor, err
		}
		latest = revision
		if err := ctx.Err(); err != nil {
			return 0, 0, err
		}
	}
}

// AppendConfigRevisionCAS assigns expected+1 only if expected is still the
// durable head. The krun_meta row is a tiny compare-and-set register; unlike a
// process mutex it coordinates every control-plane replica, and unlike an
// advisory lock it does not elect or serialize an unrelated worker.
//
// When appended is false, revision is the head that won the race. Callers can
// reload that projection and retry from it.
func (s *Store) AppendConfigRevisionCAS(ctx context.Context, expected int64, digest string, payload, ops []byte, retain int) (revision int64, floor int64, appended bool, err error) {
	if retain < 1 {
		retain = 1
	}
	if s.pool != nil {
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return 0, 0, false, err
		}
		defer tx.Rollback(ctx)
		// Repair the CAS register for upgraded or manually recovered databases.
		if _, err = tx.Exec(ctx, `INSERT INTO krun_meta (key, value)
			SELECT 'worker_config_head', COALESCE(MAX(config_revision),0)::text FROM worker_config_revisions
			ON CONFLICT (key) DO UPDATE SET value=GREATEST(krun_meta.value::bigint, excluded.value::bigint)::text`); err != nil {
			return 0, 0, false, err
		}
		revision = expected + 1
		tag, err := tx.Exec(ctx, `UPDATE krun_meta SET value=$2
			WHERE key='worker_config_head' AND value=$1`, strconv.FormatInt(expected, 10), strconv.FormatInt(revision, 10))
		if err != nil {
			return 0, 0, false, err
		}
		if tag.RowsAffected() == 0 {
			if err = tx.QueryRow(ctx, `SELECT value::bigint FROM krun_meta WHERE key='worker_config_head'`).Scan(&revision); err != nil {
				return 0, 0, false, err
			}
			if err = tx.QueryRow(ctx, `SELECT COALESCE(MIN(config_revision),0) FROM worker_config_revisions`).Scan(&floor); err != nil {
				return 0, 0, false, err
			}
			if err = tx.Commit(ctx); err != nil {
				return 0, 0, false, err
			}
			return revision, floor, false, nil
		}
		if err = replacePublishedWorkerConfig(ctx, tx, payload); err != nil {
			return 0, 0, false, err
		}
		floor = revision - int64(retain) + 1
		if floor < 1 {
			floor = 1
		}
		if _, err = tx.Exec(ctx, `WITH inserted_revision AS (
			INSERT INTO worker_config_revisions (config_revision, digest) VALUES ($1,$2)
			RETURNING config_revision
		), inserted_ops AS (
			INSERT INTO worker_config_ops (config_revision, ordinal, pool, op_type, host, release_id, payload)
			SELECT inserted_revision.config_revision, (item.ordinality-1)::integer,
				COALESCE(NULLIF(item.value->>'pool',''), item.value#>>'{route,pool}', item.value#>>'{release,pool}', 'default'),
				item.value->>'type',
				COALESCE(item.value->>'host', item.value#>>'{route,host}', ''),
				COALESCE(item.value#>>'{release,id}', item.value#>>'{route,release}', ''),
				CASE item.value->>'type'
					WHEN 'release_put' THEN item.value->'release'
					WHEN 'route_upsert' THEN item.value->'route'
					WHEN 'route_delete' THEN NULL
				END
			FROM inserted_revision,
				LATERAL jsonb_array_elements($3::jsonb) WITH ORDINALITY AS item(value, ordinality)
			RETURNING config_revision
		), deleted_ops AS (
			DELETE FROM worker_config_ops WHERE config_revision < $4
			RETURNING config_revision
		)
		DELETE FROM worker_config_revisions WHERE config_revision < $4`, revision, digest, ops, floor); err != nil {
			return 0, 0, false, err
		}
		if err = tx.Commit(ctx); err != nil {
			return 0, 0, false, err
		}
		return revision, floor, true, nil
	}
	s.configMu.Lock()
	defer s.configMu.Unlock()
	if s.configSeq != expected {
		floor := int64(0)
		if len(s.configRevs) > 0 {
			floor = s.configRevs[0].Revision
		}
		return s.configSeq, floor, false, nil
	}
	s.configSeq = expected + 1
	revision = s.configSeq
	s.configRevs = append(s.configRevs, workerConfigRevision{Revision: revision, Digest: digest, Payload: payload, Ops: ops, Created: now()})
	floor = revision - int64(retain) + 1
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
	return revision, floor, true, nil
}

// LatestConfigRevision returns the most recent projection, if any.
func (s *Store) LatestConfigRevision(ctx context.Context) (workerConfigRevision, bool, error) {
	if s.pool != nil {
		var out workerConfigRevision
		var payload, ops []byte
		err := s.pool.QueryRow(ctx, `SELECT revision.config_revision, revision.digest,
			COALESCE((SELECT jsonb_agg(jsonb_strip_nulls(jsonb_build_object(
				'type', op.op_type,
				'pool', op.pool,
				'host', NULLIF(op.host,''),
				'route', CASE WHEN op.op_type='route_upsert' THEN op.payload END,
				'release', CASE WHEN op.op_type='release_put' THEN op.payload END
			)) ORDER BY op.ordinal) FROM worker_config_ops op
				WHERE op.config_revision=revision.config_revision), '[]'::jsonb),
			revision.created_at,
			jsonb_build_object(
				'routes', COALESCE((
					SELECT jsonb_agg(jsonb_strip_nulls(jsonb_build_object(
						'host', host, 'pool', pool, 'path', NULLIF(path,''), 'enabled', enabled, 'release', release_id
					)) ORDER BY host) FROM worker_config_routes
				), '[]'::jsonb),
				'releases', COALESCE((
					SELECT jsonb_object_agg(release_id, payload ORDER BY release_id)
					FROM (SELECT DISTINCT ON (release_id) release_id, payload
						FROM worker_config_releases ORDER BY release_id, pool) releases
				), '{}'::jsonb)
			)
			FROM worker_config_revisions revision ORDER BY revision.config_revision DESC LIMIT 1`).Scan(&out.Revision, &out.Digest, &ops, &out.Created, &payload)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				var head, entities int64
				if checkErr := s.pool.QueryRow(ctx, `SELECT
					COALESCE((SELECT value::bigint FROM krun_meta WHERE key='worker_config_head'),0),
					(SELECT count(*) FROM worker_config_routes) +
					(SELECT count(*) FROM worker_config_releases) +
					(SELECT count(*) FROM worker_config_ops)`).Scan(&head, &entities); checkErr != nil {
					return workerConfigRevision{}, false, checkErr
				}
				if head != 0 || entities != 0 {
					return workerConfigRevision{}, false, errors.New("edge config normalized head exists without revision metadata")
				}
				return workerConfigRevision{}, false, nil
			}
			return workerConfigRevision{}, false, err
		}
		projection, decodeErr := decodePublishedWorkerConfig(payload)
		if decodeErr != nil {
			return workerConfigRevision{}, false, decodeErr
		}
		reconstructedDigest, _, digestErr := workerConfigDigest(projection)
		if digestErr != nil {
			return workerConfigRevision{}, false, digestErr
		}
		if reconstructedDigest != out.Digest {
			return workerConfigRevision{}, false, errors.New("edge config head digest does not match normalized projection")
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
		rows, err := s.pool.Query(ctx, `SELECT revision.config_revision, revision.digest,
			COALESCE((SELECT jsonb_agg(jsonb_strip_nulls(jsonb_build_object(
				'type', op.op_type,
				'pool', op.pool,
				'host', NULLIF(op.host,''),
				'route', CASE WHEN op.op_type='route_upsert' THEN op.payload END,
				'release', CASE WHEN op.op_type='release_put' THEN op.payload END
			)) ORDER BY op.ordinal) FROM worker_config_ops op
				WHERE op.config_revision=revision.config_revision), '[]'::jsonb),
			revision.created_at
			FROM worker_config_revisions revision
			WHERE revision.config_revision > $1 ORDER BY revision.config_revision LIMIT $2`, since, limit)
		if err != nil {
			return nil, 0, err
		}
		defer rows.Close()
		out := []workerConfigRevision{}
		for rows.Next() {
			var item workerConfigRevision
			var ops []byte
			if err := rows.Scan(&item.Revision, &item.Digest, &ops, &item.Created); err != nil {
				return nil, 0, err
			}
			item.Ops = ops
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
		s.acks = map[string]WorkerAck{}
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
