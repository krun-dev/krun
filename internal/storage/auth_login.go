package storage

import (
	"context"
	sql "database/sql"
	"errors"
	"sort"
	"time"

	models "github.com/krun-dev/krun/internal/storage/models"

	"github.com/jackc/pgx/v5"
	"github.com/krun-dev/krun/internal/domain"
)

// beginLoginState advances one browser generation and inserts the OAuth state
// in the same transaction. Concurrent starts cannot create two valid states at
// the same generation.
func (s *Store) BeginLoginState(ctx context.Context, browserHash string, expected domain.Browser, stateHash string, login domain.Login) (domain.Login, error) {
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
		return domain.Login{}, domain.ForbiddenCode("AUTH_STATE_INVALID", "Login state changed")
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
		return domain.EmailLogin{}, domain.ForbiddenCode("AUTH_STATE_INVALID", "Login state changed")
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
	var row models.LoginState
	err := s.orm.NewSelect().Model(&row).Where("state_hash = ?", hash).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Login{}, false, nil
	}
	if err != nil {
		return domain.Login{}, false, err
	}
	return domain.Login{Provider: row.Provider, Browser: row.BrowserHash, Fingerprint: row.Fingerprint, Verifier: row.Verifier, Nonce: row.Nonce, ReturnTo: row.ReturnTo, Generation: row.Generation, Expires: row.ExpiresAt, Consumed: row.Consumed}, true, nil
}

// consumeLoginState marks a login transaction used. The conditional update is
// what makes a replayed callback lose: rows affected is 0 when it was taken.
func (s *Store) ConsumeLoginState(ctx context.Context, hash string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE krun_login_states SET consumed=true WHERE state_hash=$1 AND consumed=false`, hash)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (s *Store) EmailLogin(ctx context.Context, hash string) (domain.EmailLogin, bool, error) {
	var row models.EmailLogin
	err := s.orm.NewSelect().Model(&row).Where("challenge_hash = ?", hash).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.EmailLogin{}, false, nil
	}
	if err != nil {
		return domain.EmailLogin{}, false, err
	}
	return domain.EmailLogin{Provider: row.Provider, Browser: row.BrowserHash, Fingerprint: row.Fingerprint, Email: row.Email, CodeHash: row.CodeHash, ReturnTo: row.ReturnTo, IP: row.IP, Generation: row.Generation, Attempts: row.Attempts, Sent: row.SentAt, Expires: row.ExpiresAt, Consumed: row.Consumed}, true, nil
}

// invalidateEmailLogin makes a challenge unusable while retaining it for the
// send cooldown. It is used when delivery fails after the row was inserted.
func (s *Store) InvalidateEmailLogin(ctx context.Context, hash string) error {
	_, err := s.orm.NewUpdate().Model((*models.EmailLogin)(nil)).
		Set("consumed = TRUE").
		Where("challenge_hash = ? AND consumed = FALSE", hash).
		Exec(ctx)
	return err
}

// failEmailLogin counts a failed verification attempt only while the
// challenge is live. The conditional update prevents concurrent requests from
// losing increments or exceeding the attempt budget with stale snapshots.
func (s *Store) FailEmailLogin(ctx context.Context, hash string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE krun_email_logins SET attempts=attempts+1 WHERE challenge_hash=$1 AND consumed=false AND attempts < 5 AND expires_at > now()`, hash)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}
