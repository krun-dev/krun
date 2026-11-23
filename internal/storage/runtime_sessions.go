package storage

import (
	"context"
	"errors"

	"github.com/krun-dev/krun/internal/domain"
)

func (s *Store) OpenRuntimeSession(ctx context.Context, pool, pod, epoch, session string) error {
	if pod == "" || len(pod) > 128 {
		return domain.Invalid("SESSION_INVALID", "Invalid runtime session identity")
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO worker_runtime_sessions(pod_uid,pool,config_epoch,session_id) VALUES($1,$2,$3,$4)
		ON CONFLICT(pod_uid) DO UPDATE SET pool=excluded.pool,config_epoch=excluded.config_epoch,session_id=excluded.session_id,
		inventory_complete=false,connected=true,fresh=false,rejected=false,updated_at=now()`, pod, pool, epoch, session)
	return err
}

func (s *Store) TouchRuntimeSession(ctx context.Context, pod, session string, complete, fresh bool) error {
	tag, err := s.pool.Exec(ctx, `UPDATE worker_runtime_sessions SET inventory_complete=$3,fresh=$4,updated_at=now()
		WHERE pod_uid=$1 AND session_id=$2 AND connected`, pod, session, complete, fresh)
	if err == nil && tag.RowsAffected() != 1 {
		return errors.New("runtime session was replaced or expired")
	}
	return err
}

func (s *Store) CloseRuntimeSession(ctx context.Context, pod, session string, rejected bool) error {
	_, err := s.pool.Exec(ctx, `UPDATE worker_runtime_sessions SET connected=false,fresh=false,rejected=$3,updated_at=now() WHERE pod_uid=$1 AND session_id=$2`, pod, session, rejected)
	return err
}

// RuntimePropagation counts a trusted membership snapshot supplied by the
// Kubernetes adapter. Missing sessions are unknown, never silently excluded.
func (s *Store) RuntimePropagation(ctx context.Context, pool, worker, environment string, pods []string, confirmed bool) (domain.RuntimePropagation, error) {
	out := domain.RuntimePropagation{MembershipConfirmed: confirmed, State: "unknown", Total: len(pods)}
	if err := s.pool.QueryRow(ctx, `SELECT m.value,COALESCE(g.generation,1),COALESCE(g.incarnation,md5('deployment:'||e.worker_id||'/'||e.env_name))
		FROM krun_worker_environments e JOIN krun_meta m ON m.key='worker_config_epoch'
		LEFT JOIN worker_runtime_generations g ON g.kind='deployment' AND g.resource_id=e.worker_id||'/'||e.env_name
		WHERE e.worker_id=$1 AND e.env_name=$2`, worker, environment).Scan(&out.Epoch, &out.Generation, &out.Incarnation); err != nil {
		return out, err
	}
	if !confirmed {
		rows, err := s.pool.Query(ctx, `SELECT pod_uid FROM worker_runtime_sessions WHERE pool=$1 AND updated_at>now()-interval '1 minute' ORDER BY pod_uid`, pool)
		if err != nil {
			return out, err
		}
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return out, err
			}
			pods = append(pods, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return out, err
		}
		out.Total = len(pods)
	}
	rows, err := s.pool.Query(ctx, `SELECT s.pod_uid,s.connected,s.fresh,s.inventory_complete,s.rejected,
		s.config_epoch=s2.epoch,s.updated_at>now()-interval '15 seconds',a.incarnation,a.generation
		FROM unnest($1::text[]) wanted(pod) LEFT JOIN worker_runtime_sessions s ON s.pod_uid=wanted.pod AND s.pool=$2
		CROSS JOIN (SELECT $3::text AS epoch) s2 LEFT JOIN worker_runtime_resource_acks a ON a.pod_uid=s.pod_uid AND a.session_id=s.session_id
		AND a.config_epoch=s.config_epoch AND a.pool=s.pool AND a.kind='deployment' AND a.resource_id=$4`, pods, pool, out.Epoch, worker+"/"+environment)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var pod, incarnation *string
		var connected, fresh, complete, rejected, sameEpoch, recent *bool
		var generation *int64
		if err = rows.Scan(&pod, &connected, &fresh, &complete, &rejected, &sameEpoch, &recent, &incarnation, &generation); err != nil {
			return out, err
		}
		if rejected != nil && *rejected {
			out.Rejected++
			continue
		}
		if pod == nil || connected == nil || !*connected || fresh == nil || !*fresh || complete == nil || !*complete || sameEpoch == nil || !*sameEpoch || recent == nil || !*recent {
			out.Unknown++
			continue
		}
		if generation == nil {
			out.Unsubscribed++
			continue
		}
		if incarnation != nil && *incarnation == out.Incarnation && *generation >= out.Generation {
			out.Applied++
		} else {
			out.Lagging++
		}
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	if confirmed && out.Total > 0 && out.Unknown == 0 && out.Rejected == 0 {
		if out.Lagging == 0 {
			out.State = "current"
		} else {
			out.State = "pending"
		}
	}
	return out, nil
}

// LegacyRuntimeActive bounds compatibility publishing to a rolling upgrade.
func (s *Store) LegacyRuntimeActive(ctx context.Context) (bool, error) {
	var active bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM worker_runtime_acks WHERE updated_at>now()-interval '15 seconds')`).Scan(&active)
	return active, err
}

func (s *Store) PruneRuntimeResourceAcks(ctx context.Context, pod, session string, refs []domain.ResourceRef) error {
	keys := make([]string, 0, len(refs))
	for _, ref := range refs {
		keys = append(keys, ref.Key())
	}
	_, err := s.pool.Exec(ctx, `DELETE FROM worker_runtime_resource_acks WHERE pod_uid=$1 AND session_id=$2 AND NOT(kind||':'||resource_id=ANY($3::text[]))`, pod, session, keys)
	return err
}
