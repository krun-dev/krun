package storage

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

const rateLimitRetention = 24 * time.Hour

// RateLimitExceeded atomically increments one fixed-window bucket and reports
// whether its limit has been exceeded. Only a digest of key is persisted, so
// client IPs and login identifiers do not become durable metadata.
func (s *Store) RateLimitExceeded(ctx context.Context, scope, key string, window time.Duration, limit int) (bool, error) {
	if scope == "" || key == "" || window <= 0 || limit <= 0 {
		return false, errors.New("invalid rate limit")
	}
	windowStart := now().UTC().Truncate(window)
	keyHash := digest(key)
	var count int
	err := s.pool.QueryRow(ctx, `INSERT INTO krun_rate_limits (scope, key_hash, window_start, count)
			VALUES ($1,$2,$3,1)
			ON CONFLICT (scope, key_hash, window_start)
			DO UPDATE SET count=krun_rate_limits.count+1
			WHERE krun_rate_limits.count <= $4
			RETURNING count`, scope, keyHash, windowStart, limit).Scan(&count)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil
	}
	return count > limit, err
}
