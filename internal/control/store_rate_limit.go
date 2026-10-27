package control

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

const rateLimitRetention = 24 * time.Hour

type rateLimitBucket struct {
	scope       string
	keyHash     string
	windowStart time.Time
}

// RateLimitExceeded atomically increments one fixed-window bucket and reports
// whether its limit has been exceeded. Only a digest of key is persisted, so
// client IPs and login identifiers do not become durable metadata.
func (s *Store) RateLimitExceeded(ctx context.Context, scope, key string, window time.Duration, limit int) (bool, error) {
	if scope == "" || key == "" || window <= 0 || limit <= 0 {
		return false, errors.New("invalid rate limit")
	}
	windowStart := now().UTC().Truncate(window)
	keyHash := digest(key)
	if s.pool != nil {
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
	if err := ctx.Err(); err != nil {
		return false, err
	}
	// The in-memory limiter has its own mutex. Do not acquire Store.mu here:
	// protected handlers already hold it while mutating their request-local
	// snapshot, and nested acquisition would deadlock tests.
	s.rateMu.Lock()
	defer s.rateMu.Unlock()
	if s.memoryRateLimits == nil {
		s.memoryRateLimits = make(map[rateLimitBucket]int)
	}
	cutoff := now().Add(-rateLimitRetention)
	for bucket := range s.memoryRateLimits {
		if bucket.windowStart.Before(cutoff) {
			delete(s.memoryRateLimits, bucket)
		}
	}
	bucket := rateLimitBucket{scope: scope, keyHash: keyHash, windowStart: windowStart}
	if s.memoryRateLimits[bucket] <= limit {
		s.memoryRateLimits[bucket]++
	}
	return s.memoryRateLimits[bucket] > limit, nil
}

// deleteRateLimitsBefore is called by the retention loop. Keeping the helper
// here lets retention wiring remain independent from the request path.
func (s *Store) deleteRateLimitsBefore(ctx context.Context, cutoff time.Time) error {
	if s.pool != nil {
		_, err := s.pool.Exec(ctx, `DELETE FROM krun_rate_limits WHERE window_start < $1`, cutoff)
		return err
	}
	s.rateMu.Lock()
	defer s.rateMu.Unlock()
	for bucket := range s.memoryRateLimits {
		if bucket.windowStart.Before(cutoff) {
			delete(s.memoryRateLimits, bucket)
		}
	}
	return nil
}
