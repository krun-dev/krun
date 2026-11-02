package storage

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/krun-dev/krun/internal/domain"
)

// beginLoginState advances one browser generation and inserts the OAuth state
// in the same transaction. Concurrent starts cannot create two valid states at
// the same generation.
func (s *Store) BeginLoginState(ctx context.Context, browserHash string, expected domain.Browser, stateHash string, login domain.Login) (domain.Login, error) {
	if s.pool == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.memory == nil {
			return domain.Login{}, errors.New("store is not initialized")
		}
		current, ok := s.memory.Browsers[browserHash]
		if !ok || current.Generation != expected.Generation || current.CSRF != expected.CSRF || !now().Before(current.Expires) {
			return domain.Login{}, domain.Problem(403, "AUTH_STATE_INVALID", "Login state changed")
		}
		current.Generation++
		s.memory.Browsers[browserHash] = current
		login.Generation = current.Generation
		s.memory.Logins[stateHash] = login
		return login, nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Login{}, err
	}
	defer tx.Rollback(ctx)
	var generation int
	err = tx.QueryRow(ctx, `UPDATE krun_browsers SET generation=generation+1
		WHERE browser_hash=$1 AND generation=$2 AND csrf=$3 AND expires_at > now()
		RETURNING generation`, browserHash, expected.Generation, expected.CSRF).Scan(&generation)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Login{}, domain.Problem(403, "AUTH_STATE_INVALID", "Login state changed")
	}
	if err != nil {
		return domain.Login{}, err
	}
	login.Generation = generation
	if _, err = tx.Exec(ctx, `INSERT INTO krun_login_states
		(state_hash, provider, browser_hash, fingerprint, verifier, nonce, return_to, generation, expires_at, consumed)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, stateHash, login.Provider, login.Browser,
		login.Fingerprint, login.Verifier, login.Nonce, login.ReturnTo, login.Generation, login.Expires, login.Consumed); err != nil {
		return domain.Login{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.Login{}, err
	}
	return login, nil
}

type emailSendLock struct{ scope, hash string }

// beginEmailLogin serializes the email, browser and IP checks on small hashed
// lock rows, then advances the browser generation and inserts the challenge in
// that transaction. This closes the count-then-insert race without storing raw
// email addresses or client IPs in the lock table.
func (s *Store) BeginEmailLogin(ctx context.Context, browserHash string, expected domain.Browser, challengeHash string, login domain.EmailLogin, window time.Duration, ipLimit int) (domain.EmailLogin, error) {
	if s.pool == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.memory == nil {
			return domain.EmailLogin{}, errors.New("store is not initialized")
		}
		current, ok := s.memory.Browsers[browserHash]
		if !ok || current.Generation != expected.Generation || current.CSRF != expected.CSRF || !now().Before(current.Expires) {
			return domain.EmailLogin{}, domain.Problem(403, "AUTH_STATE_INVALID", "Login state changed")
		}
		since := now().Add(-window)
		fromIP := 0
		for _, item := range s.memory.EmailLogins {
			if item.Sent.Before(since) {
				continue
			}
			if item.Email == login.Email || item.Browser == browserHash {
				return domain.EmailLogin{}, domain.Problem(429, "RATE_LIMITED", "Wait 60 seconds before requesting another code")
			}
			if item.IP == login.IP {
				fromIP++
			}
		}
		if fromIP >= ipLimit {
			return domain.EmailLogin{}, domain.Problem(429, "RATE_LIMITED", "Try again later")
		}
		current.Generation++
		current.Expires = now().Add(15 * time.Minute)
		s.memory.Browsers[browserHash] = current
		login.Generation = current.Generation
		s.memory.EmailLogins[challengeHash] = login
		return login, nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.EmailLogin{}, err
	}
	defer tx.Rollback(ctx)
	locks := []emailSendLock{
		{scope: "browser", hash: digest(browserHash)},
		{scope: "email", hash: digest(login.Email)},
		{scope: "ip", hash: digest(login.IP)},
	}
	sort.Slice(locks, func(i, j int) bool {
		if locks[i].scope == locks[j].scope {
			return locks[i].hash < locks[j].hash
		}
		return locks[i].scope < locks[j].scope
	})
	for _, lock := range locks {
		if _, err = tx.Exec(ctx, `INSERT INTO krun_email_send_locks (scope, key_hash, touched_at)
			VALUES ($1,$2,now()) ON CONFLICT (scope,key_hash) DO UPDATE SET touched_at=excluded.touched_at`, lock.scope, lock.hash); err != nil {
			return domain.EmailLogin{}, err
		}
	}
	since := now().Add(-window)
	var fromIP int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM krun_email_logins WHERE ip=$1 AND sent_at >= $2`, login.IP, since).Scan(&fromIP); err != nil {
		return domain.EmailLogin{}, err
	}
	if fromIP >= ipLimit {
		return domain.EmailLogin{}, domain.Problem(429, "RATE_LIMITED", "Try again later")
	}
	var recent bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM krun_email_logins WHERE sent_at >= $1 AND (email=$2 OR browser_hash=$3)
	)`, since, login.Email, browserHash).Scan(&recent); err != nil {
		return domain.EmailLogin{}, err
	}
	if recent {
		return domain.EmailLogin{}, domain.Problem(429, "RATE_LIMITED", "Wait 60 seconds before requesting another code")
	}
	var generation int
	err = tx.QueryRow(ctx, `UPDATE krun_browsers SET generation=generation+1, expires_at=$4
		WHERE browser_hash=$1 AND generation=$2 AND csrf=$3 AND expires_at > now()
		RETURNING generation`, browserHash, expected.Generation, expected.CSRF, now().Add(15*time.Minute)).Scan(&generation)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.EmailLogin{}, domain.Problem(403, "AUTH_STATE_INVALID", "Login state changed")
	}
	if err != nil {
		return domain.EmailLogin{}, err
	}
	login.Generation = generation
	if _, err = tx.Exec(ctx, `INSERT INTO krun_email_logins
		(challenge_hash, provider, browser_hash, fingerprint, email, code_hash, return_to, ip, generation, attempts, sent_at, expires_at, consumed)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, challengeHash, login.Provider,
		login.Browser, login.Fingerprint, login.Email, login.CodeHash, login.ReturnTo, login.IP,
		login.Generation, login.Attempts, login.Sent, login.Expires, login.Consumed); err != nil {
		return domain.EmailLogin{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.EmailLogin{}, err
	}
	return login, nil
}

func (s *Store) LoginState(ctx context.Context, hash string) (domain.Login, bool, error) {
	if s.pool == nil {
		login, ok := s.memState().Logins[hash]
		return login, ok, nil
	}
	var out domain.Login
	err := s.pool.QueryRow(ctx, `SELECT provider, browser_hash, fingerprint, verifier, nonce, return_to, generation, expires_at, consumed FROM krun_login_states WHERE state_hash=$1`, hash).Scan(&out.Provider, &out.Browser, &out.Fingerprint, &out.Verifier, &out.Nonce, &out.ReturnTo, &out.Generation, &out.Expires, &out.Consumed)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Login{}, false, nil
	}
	if err != nil {
		return domain.Login{}, false, err
	}
	return out, true, nil
}

func (s *Store) insertLoginState(ctx context.Context, hash string, login domain.Login) error {
	if s.pool == nil {
		if st := s.memState(); st != nil {
			st.Logins[hash] = login
		}
		return nil
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO krun_login_states (state_hash, provider, browser_hash, fingerprint, verifier, nonce, return_to, generation, expires_at, consumed) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, hash, login.Provider, login.Browser, login.Fingerprint, login.Verifier, login.Nonce, login.ReturnTo, login.Generation, login.Expires, login.Consumed)
	return err
}

// consumeLoginState marks a login transaction used. The conditional update is
// what makes a replayed callback lose: rows affected is 0 when it was taken.
func (s *Store) ConsumeLoginState(ctx context.Context, hash string) (bool, error) {
	if s.pool == nil {
		st := s.memState()
		login, ok := st.Logins[hash]
		if !ok || login.Consumed {
			return false, nil
		}
		login.Consumed = true
		st.Logins[hash] = login
		return true, nil
	}
	tag, err := s.pool.Exec(ctx, `UPDATE krun_login_states SET consumed=true WHERE state_hash=$1 AND consumed=false`, hash)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (s *Store) deleteLoginState(ctx context.Context, hash string) error {
	if hash == "" {
		return nil
	}
	if s.pool == nil {
		if st := s.memState(); st != nil {
			delete(st.Logins, hash)
		}
		return nil
	}
	_, err := s.pool.Exec(ctx, `DELETE FROM krun_login_states WHERE state_hash=$1`, hash)
	return err
}

func (s *Store) EmailLogin(ctx context.Context, hash string) (domain.EmailLogin, bool, error) {
	if s.pool == nil {
		login, ok := s.memState().EmailLogins[hash]
		return login, ok, nil
	}
	var out domain.EmailLogin
	err := s.pool.QueryRow(ctx, `SELECT provider, browser_hash, fingerprint, email, code_hash, return_to, ip, generation, attempts, sent_at, expires_at, consumed FROM krun_email_logins WHERE challenge_hash=$1`, hash).Scan(&out.Provider, &out.Browser, &out.Fingerprint, &out.Email, &out.CodeHash, &out.ReturnTo, &out.IP, &out.Generation, &out.Attempts, &out.Sent, &out.Expires, &out.Consumed)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.EmailLogin{}, false, nil
	}
	if err != nil {
		return domain.EmailLogin{}, false, err
	}
	return out, true, nil
}

func (s *Store) insertEmailLogin(ctx context.Context, hash string, login domain.EmailLogin) error {
	if s.pool == nil {
		if st := s.memState(); st != nil {
			st.EmailLogins[hash] = login
		}
		return nil
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO krun_email_logins (challenge_hash, provider, browser_hash, fingerprint, email, code_hash, return_to, ip, generation, attempts, sent_at, expires_at, consumed) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, hash, login.Provider, login.Browser, login.Fingerprint, login.Email, login.CodeHash, login.ReturnTo, login.IP, login.Generation, login.Attempts, login.Sent, login.Expires, login.Consumed)
	return err
}

// invalidateEmailLogin makes a challenge unusable while retaining it for the
// send cooldown. It is used when delivery fails after the row was inserted.
func (s *Store) InvalidateEmailLogin(ctx context.Context, hash string) error {
	if s.pool == nil {
		if st := s.memState(); st != nil {
			login, ok := st.EmailLogins[hash]
			if ok && !login.Consumed {
				login.Consumed = true
				st.EmailLogins[hash] = login
			}
		}
		return nil
	}
	_, err := s.pool.Exec(ctx, `UPDATE krun_email_logins SET consumed=true WHERE challenge_hash=$1 AND consumed=false`, hash)
	return err
}

// failEmailLogin counts a failed verification attempt only while the
// challenge is live. The conditional update prevents concurrent requests from
// losing increments or exceeding the attempt budget with stale snapshots.
func (s *Store) FailEmailLogin(ctx context.Context, hash string) (bool, error) {
	if s.pool == nil {
		st := s.memState()
		login, ok := st.EmailLogins[hash]
		if !ok || login.Consumed || login.Attempts >= 5 || !now().Before(login.Expires) {
			return false, nil
		}
		login.Attempts++
		st.EmailLogins[hash] = login
		return true, nil
	}
	tag, err := s.pool.Exec(ctx, `UPDATE krun_email_logins SET attempts=attempts+1 WHERE challenge_hash=$1 AND consumed=false AND attempts < 5 AND expires_at > now()`, hash)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// consumeEmailLogin atomically wins a verification challenge. A second
// correct request observes zero affected rows and cannot issue another
// session, even when it validated an earlier snapshot concurrently.
func (s *Store) consumeEmailLogin(ctx context.Context, hash string) (bool, error) {
	if s.pool == nil {
		st := s.memState()
		login, ok := st.EmailLogins[hash]
		if !ok || login.Consumed || login.Attempts >= 5 || !now().Before(login.Expires) {
			return false, nil
		}
		login.Attempts++
		login.Consumed = true
		st.EmailLogins[hash] = login
		return true, nil
	}
	tag, err := s.pool.Exec(ctx, `UPDATE krun_email_logins SET attempts=attempts+1, consumed=true WHERE challenge_hash=$1 AND consumed=false AND attempts < 5 AND expires_at > now()`, hash)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// emailSendBudget reports how many codes this browser and IP requested inside
// the cooldown window. Counting in SQL replaces the full-table scan the
// in-memory version performed on every send.
func (s *Store) emailSendBudget(ctx context.Context, ip, email, browserHash string, window time.Duration) (byIP int, recent bool, err error) {
	if s.pool == nil {
		st := s.memState()
		since := now().Add(-window)
		for _, v := range st.EmailLogins {
			if now().Sub(v.Sent) >= window {
				continue
			}
			if v.IP == ip && v.Sent.After(since) {
				byIP++
			}
			if v.Sent.After(since) && (v.Email == email || v.Browser == browserHash) {
				recent = true
			}
		}
		return byIP, recent, nil
	}
	since := now().Add(-window)
	if err = s.pool.QueryRow(ctx, `SELECT count(*) FROM krun_email_logins WHERE ip=$1 AND sent_at >= $2`, ip, since).Scan(&byIP); err != nil {
		return 0, false, err
	}
	var found bool
	if err = s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM krun_email_logins WHERE sent_at >= $1 AND (email=$2 OR browser_hash=$3))`, since, email, browserHash).Scan(&found); err != nil {
		return 0, false, err
	}
	return byIP, found, nil
}
