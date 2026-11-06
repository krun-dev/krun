package storage

import "context"

func (s *Store) SetClaimedBuildLogKey(ctx context.Context, id, owner, key string) error {
	if s.pool == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		b, ok := s.memory.Workers.Builds[id]
		if !ok || b.ClaimedBy != owner || workerBuildTerminal(b.State) {
			return ErrBuildClaimLost
		}
		p := s.memory.Workers.BuildPrivate[id]
		p.LogKey = key
		s.memory.Workers.BuildPrivate[id] = p
		return nil
	}
	tag, err := s.pool.Exec(ctx, `WITH owned AS (
 UPDATE krun_worker_builds SET claimed_at=claimed_at
 WHERE build_id=$1 AND claimed_by=$2 AND state IN ('building','fetching') RETURNING build_id
 ) UPDATE krun_worker_build_private SET log_key=$3 WHERE build_id IN (SELECT build_id FROM owned)`, id, owner, key)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrBuildClaimLost
	}
	return nil
}
