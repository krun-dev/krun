package control

import (
	"context"
	"crypto/subtle"
	"errors"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// Sessions, browsers and login transactions are read and written with
// single-row statements. They are never part of an Update partition, so an
// authenticated request that only refreshes last-seen cost is one conditional
// UPDATE instead of a whole-state rewrite.
//
// seen_at is throttled: at most one write per session per seenThrottle. Without
// this, every authenticated request would be a write.

const seenThrottle = 30 * time.Second

var errDeviceCodeCollision = errors.New("device code collision")

// apiKeyIDByHash resolves only the presented credential. Key hashes are never
// loaded into the general IAM snapshot, which keeps every authenticated read
// from materializing all long-lived secrets in the process.
func (s *Store) apiKeyIDByHash(ctx context.Context, hash string) (string, bool, error) {
	if hash == "" {
		return "", false, nil
	}
	if s.pool == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.memory == nil {
			return "", false, errors.New("store is not initialized")
		}
		for id, candidate := range s.memory.KeyHashes {
			if subtle.ConstantTimeCompare([]byte(hash), []byte(candidate)) == 1 {
				return id, true, nil
			}
		}
		return "", false, nil
	}
	var id string
	err := s.pool.QueryRow(ctx, `SELECT key_id FROM krun_api_key_hashes WHERE hash=$1 LIMIT 1`, hash).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return id, true, nil
}

func (s *Store) sessionByHash(ctx context.Context, hash string) (Session, bool, error) {
	if hash == "" {
		return Session{}, false, nil
	}
	if s.pool == nil {
		session, ok := s.memState().Sessions[hash]
		return session, ok, nil
	}
	var out Session
	err := s.pool.QueryRow(ctx, `SELECT session_hash, user_id, csrf, auth_version, cli, created_at, seen_at, absolute_at FROM krun_sessions WHERE session_hash=$1`, hash).Scan(&out.Hash, &out.User, &out.CSRF, &out.AuthVersion, &out.CLI, &out.Created, &out.Seen, &out.Absolute)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, false, nil
	}
	if err != nil {
		return Session{}, false, err
	}
	return out, true, nil
}

// touchSession refreshes seen_at only when the stored value is older than the
// throttle. It deliberately does not fail the request when it loses a race.
func (s *Store) touchSession(ctx context.Context, hash string) error {
	if s.pool == nil {
		if st := s.memState(); st != nil {
			if session, ok := st.Sessions[hash]; ok && now().Sub(session.Seen) >= seenThrottle {
				session.Seen = now()
				st.Sessions[hash] = session
			}
		}
		return nil
	}
	_, err := s.pool.Exec(ctx, `UPDATE krun_sessions SET seen_at=now() WHERE session_hash=$1 AND seen_at < now() - $2::interval`, hash, seenThrottle.String())
	return err
}

func (s *Store) insertSession(ctx context.Context, token string, session Session) error {
	if s.pool == nil {
		if st := s.memState(); st != nil {
			st.Sessions[digest(token)] = session
		}
		return nil
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO krun_sessions (session_hash, user_id, csrf, auth_version, cli, created_at, seen_at, absolute_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, digest(token), session.User, session.CSRF, session.AuthVersion, session.CLI, session.Created, session.Seen, session.Absolute)
	return err
}

func (s *Store) deleteSession(ctx context.Context, hash string) error {
	if hash == "" {
		return nil
	}
	if s.pool == nil {
		if st := s.memState(); st != nil {
			delete(st.Sessions, hash)
		}
		return nil
	}
	_, err := s.pool.Exec(ctx, `DELETE FROM krun_sessions WHERE session_hash=$1`, hash)
	return err
}

// browserFor reads one browser row. A missing row is not an error: the caller
// decides whether to issue a new browser cookie.
func (s *Store) browserFor(ctx context.Context, hash string) (Browser, bool, error) {
	if hash == "" {
		return Browser{}, false, nil
	}
	if s.pool == nil {
		browser, ok := s.memState().Browsers[hash]
		return browser, ok, nil
	}
	var out Browser
	err := s.pool.QueryRow(ctx, `SELECT csrf, generation, expires_at FROM krun_browsers WHERE browser_hash=$1`, hash).Scan(&out.CSRF, &out.Generation, &out.Expires)
	if errors.Is(err, pgx.ErrNoRows) {
		return Browser{}, false, nil
	}
	if err != nil {
		return Browser{}, false, err
	}
	return out, true, nil
}

func (s *Store) upsertBrowser(ctx context.Context, hash string, browser Browser) error {
	if s.pool == nil {
		if st := s.memState(); st != nil {
			st.Browsers[hash] = browser
		}
		return nil
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO krun_browsers (browser_hash, csrf, generation, expires_at) VALUES ($1,$2,$3,$4) ON CONFLICT (browser_hash) DO UPDATE SET csrf=excluded.csrf, generation=excluded.generation, expires_at=excluded.expires_at`, hash, browser.CSRF, browser.Generation, browser.Expires)
	return err
}

func (s *Store) deleteBrowser(ctx context.Context, hash string) error {
	if hash == "" {
		return nil
	}
	if s.pool == nil {
		if st := s.memState(); st != nil {
			delete(st.Browsers, hash)
		}
		return nil
	}
	_, err := s.pool.Exec(ctx, `DELETE FROM krun_browsers WHERE browser_hash=$1`, hash)
	return err
}

// beginLoginState advances one browser generation and inserts the OAuth state
// in the same transaction. Concurrent starts cannot create two valid states at
// the same generation.
func (s *Store) beginLoginState(ctx context.Context, browserHash string, expected Browser, stateHash string, login Login) (Login, error) {
	if s.pool == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.memory == nil {
			return Login{}, errors.New("store is not initialized")
		}
		current, ok := s.memory.Browsers[browserHash]
		if !ok || current.Generation != expected.Generation || current.CSRF != expected.CSRF || !now().Before(current.Expires) {
			return Login{}, problem(403, "AUTH_STATE_INVALID", "Login state changed")
		}
		current.Generation++
		s.memory.Browsers[browserHash] = current
		login.Generation = current.Generation
		s.memory.Logins[stateHash] = login
		return login, nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Login{}, err
	}
	defer tx.Rollback(ctx)
	var generation int
	err = tx.QueryRow(ctx, `UPDATE krun_browsers SET generation=generation+1
		WHERE browser_hash=$1 AND generation=$2 AND csrf=$3 AND expires_at > now()
		RETURNING generation`, browserHash, expected.Generation, expected.CSRF).Scan(&generation)
	if errors.Is(err, pgx.ErrNoRows) {
		return Login{}, problem(403, "AUTH_STATE_INVALID", "Login state changed")
	}
	if err != nil {
		return Login{}, err
	}
	login.Generation = generation
	if _, err = tx.Exec(ctx, `INSERT INTO krun_login_states
		(state_hash, provider, browser_hash, fingerprint, verifier, nonce, return_to, generation, expires_at, consumed)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, stateHash, login.Provider, login.Browser,
		login.Fingerprint, login.Verifier, login.Nonce, login.ReturnTo, login.Generation, login.Expires, login.Consumed); err != nil {
		return Login{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Login{}, err
	}
	return login, nil
}

type emailSendLock struct{ scope, hash string }

// beginEmailLogin serializes the email, browser and IP checks on small hashed
// lock rows, then advances the browser generation and inserts the challenge in
// that transaction. This closes the count-then-insert race without storing raw
// email addresses or client IPs in the lock table.
func (s *Store) beginEmailLogin(ctx context.Context, browserHash string, expected Browser, challengeHash string, login EmailLogin, window time.Duration, ipLimit int) (EmailLogin, error) {
	if s.pool == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.memory == nil {
			return EmailLogin{}, errors.New("store is not initialized")
		}
		current, ok := s.memory.Browsers[browserHash]
		if !ok || current.Generation != expected.Generation || current.CSRF != expected.CSRF || !now().Before(current.Expires) {
			return EmailLogin{}, problem(403, "AUTH_STATE_INVALID", "Login state changed")
		}
		since := now().Add(-window)
		fromIP := 0
		for _, item := range s.memory.EmailLogins {
			if item.Sent.Before(since) {
				continue
			}
			if item.Email == login.Email || item.Browser == browserHash {
				return EmailLogin{}, problem(429, "RATE_LIMITED", "Wait 60 seconds before requesting another code")
			}
			if item.IP == login.IP {
				fromIP++
			}
		}
		if fromIP >= ipLimit {
			return EmailLogin{}, problem(429, "RATE_LIMITED", "Try again later")
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
		return EmailLogin{}, err
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
			return EmailLogin{}, err
		}
	}
	since := now().Add(-window)
	var fromIP int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM krun_email_logins WHERE ip=$1 AND sent_at >= $2`, login.IP, since).Scan(&fromIP); err != nil {
		return EmailLogin{}, err
	}
	if fromIP >= ipLimit {
		return EmailLogin{}, problem(429, "RATE_LIMITED", "Try again later")
	}
	var recent bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM krun_email_logins WHERE sent_at >= $1 AND (email=$2 OR browser_hash=$3)
	)`, since, login.Email, browserHash).Scan(&recent); err != nil {
		return EmailLogin{}, err
	}
	if recent {
		return EmailLogin{}, problem(429, "RATE_LIMITED", "Wait 60 seconds before requesting another code")
	}
	var generation int
	err = tx.QueryRow(ctx, `UPDATE krun_browsers SET generation=generation+1, expires_at=$4
		WHERE browser_hash=$1 AND generation=$2 AND csrf=$3 AND expires_at > now()
		RETURNING generation`, browserHash, expected.Generation, expected.CSRF, now().Add(15*time.Minute)).Scan(&generation)
	if errors.Is(err, pgx.ErrNoRows) {
		return EmailLogin{}, problem(403, "AUTH_STATE_INVALID", "Login state changed")
	}
	if err != nil {
		return EmailLogin{}, err
	}
	login.Generation = generation
	if _, err = tx.Exec(ctx, `INSERT INTO krun_email_logins
		(challenge_hash, provider, browser_hash, fingerprint, email, code_hash, return_to, ip, generation, attempts, sent_at, expires_at, consumed)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, challengeHash, login.Provider,
		login.Browser, login.Fingerprint, login.Email, login.CodeHash, login.ReturnTo, login.IP,
		login.Generation, login.Attempts, login.Sent, login.Expires, login.Consumed); err != nil {
		return EmailLogin{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return EmailLogin{}, err
	}
	return login, nil
}

func (s *Store) loginState(ctx context.Context, hash string) (Login, bool, error) {
	if s.pool == nil {
		login, ok := s.memState().Logins[hash]
		return login, ok, nil
	}
	var out Login
	err := s.pool.QueryRow(ctx, `SELECT provider, browser_hash, fingerprint, verifier, nonce, return_to, generation, expires_at, consumed FROM krun_login_states WHERE state_hash=$1`, hash).Scan(&out.Provider, &out.Browser, &out.Fingerprint, &out.Verifier, &out.Nonce, &out.ReturnTo, &out.Generation, &out.Expires, &out.Consumed)
	if errors.Is(err, pgx.ErrNoRows) {
		return Login{}, false, nil
	}
	if err != nil {
		return Login{}, false, err
	}
	return out, true, nil
}

func (s *Store) insertLoginState(ctx context.Context, hash string, login Login) error {
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
func (s *Store) consumeLoginState(ctx context.Context, hash string) (bool, error) {
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

func (s *Store) emailLogin(ctx context.Context, hash string) (EmailLogin, bool, error) {
	if s.pool == nil {
		login, ok := s.memState().EmailLogins[hash]
		return login, ok, nil
	}
	var out EmailLogin
	err := s.pool.QueryRow(ctx, `SELECT provider, browser_hash, fingerprint, email, code_hash, return_to, ip, generation, attempts, sent_at, expires_at, consumed FROM krun_email_logins WHERE challenge_hash=$1`, hash).Scan(&out.Provider, &out.Browser, &out.Fingerprint, &out.Email, &out.CodeHash, &out.ReturnTo, &out.IP, &out.Generation, &out.Attempts, &out.Sent, &out.Expires, &out.Consumed)
	if errors.Is(err, pgx.ErrNoRows) {
		return EmailLogin{}, false, nil
	}
	if err != nil {
		return EmailLogin{}, false, err
	}
	return out, true, nil
}

func (s *Store) insertEmailLogin(ctx context.Context, hash string, login EmailLogin) error {
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
func (s *Store) invalidateEmailLogin(ctx context.Context, hash string) error {
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
func (s *Store) failEmailLogin(ctx context.Context, hash string) (bool, error) {
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

func (s *Store) deviceByCode(ctx context.Context, hash string) (Device, bool, error) {
	if s.pool == nil {
		device, ok := s.memState().Devices[hash]
		return device, ok, nil
	}
	var out Device
	err := s.pool.QueryRow(ctx, `SELECT code_hash, user_code, token, expires_at, approved, consumed FROM krun_device_codes WHERE code_hash=$1`, hash).Scan(&out.CodeHash, &out.UserCode, &out.Token, &out.Expires, &out.Approved, &out.Consumed)
	if errors.Is(err, pgx.ErrNoRows) {
		return Device{}, false, nil
	}
	if err != nil {
		return Device{}, false, err
	}
	return out, true, nil
}

func (s *Store) deviceByUserCode(ctx context.Context, userCode string) (Device, bool, error) {
	if s.pool == nil {
		for _, device := range s.memState().Devices {
			if device.UserCode == userCode {
				return device, true, nil
			}
		}
		return Device{}, false, nil
	}
	var out Device
	err := s.pool.QueryRow(ctx, `SELECT code_hash, user_code, token, expires_at, approved, consumed FROM krun_device_codes WHERE user_code=$1`, userCode).Scan(&out.CodeHash, &out.UserCode, &out.Token, &out.Expires, &out.Approved, &out.Consumed)
	if errors.Is(err, pgx.ErrNoRows) {
		return Device{}, false, nil
	}
	if err != nil {
		return Device{}, false, err
	}
	return out, true, nil
}

func (s *Store) insertDevice(ctx context.Context, device Device) error {
	if s.pool == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.memory == nil {
			return errors.New("store is not initialized")
		}
		if _, exists := s.memory.Devices[device.CodeHash]; exists {
			return errDeviceCodeCollision
		}
		for _, existing := range s.memory.Devices {
			if existing.UserCode == device.UserCode {
				return errDeviceCodeCollision
			}
		}
		s.memory.Devices[device.CodeHash] = device
		return nil
	}
	tag, err := s.pool.Exec(ctx, `INSERT INTO krun_device_codes (code_hash, user_code, token, expires_at, approved, consumed) VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING`, device.CodeHash, device.UserCode, device.Token, device.Expires, device.Approved, device.Consumed)
	if err == nil && tag.RowsAffected() == 0 {
		return errDeviceCodeCollision
	}
	return err
}

// consumeDevice clears the token and marks the code used in one statement, so
// two concurrent polls cannot both collect a CLI credential.
func (s *Store) consumeDevice(ctx context.Context, hash string) (string, bool, error) {
	if s.pool == nil {
		st := s.memState()
		device, ok := st.Devices[hash]
		if !ok || device.Consumed || !device.Approved || device.Token == "" || !now().Before(device.Expires) {
			return "", false, nil
		}
		token := device.Token
		device.Consumed, device.Token = true, ""
		st.Devices[hash] = device
		return token, true, nil
	}
	var token string
	err := s.pool.QueryRow(ctx, `WITH candidate AS MATERIALIZED (
		SELECT code_hash, token FROM krun_device_codes
		WHERE code_hash=$1 AND consumed=false AND approved=true AND token <> '' AND expires_at > now()
		FOR UPDATE
	)
	UPDATE krun_device_codes AS device SET consumed=true, token=''
	FROM candidate WHERE device.code_hash=candidate.code_hash
	RETURNING candidate.token`, hash).Scan(&token)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return token, true, nil
}

// approveDevice conditionally approves a live device code and creates the CLI
// session in the same transaction. A losing approval therefore cannot leave
// behind an orphan credential.
func (s *Store) approveDevice(ctx context.Context, hash, token string, session Session) (bool, error) {
	return s.approveDeviceAudited(ctx, hash, token, session, nil)
}

func (s *Store) approveDeviceAudited(ctx context.Context, hash, token string, session Session, audits []Audit) (bool, error) {
	if s.pool == nil {
		st := s.memState()
		if st == nil {
			return false, errors.New("store is not initialized")
		}
		device, ok := st.Devices[hash]
		if !ok || device.Approved || device.Consumed || !now().Before(device.Expires) {
			return false, nil
		}
		device.Approved, device.Token = true, token
		st.Devices[hash] = device
		st.Sessions[digest(token)] = session
		return true, nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE krun_device_codes SET approved=true, token=$2 WHERE code_hash=$1 AND consumed=false AND approved=false AND expires_at > now()`, hash, token)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() != 1 {
		return false, nil
	}
	if _, err = tx.Exec(ctx, `INSERT INTO krun_sessions (session_hash, user_id, csrf, auth_version, cli, created_at, seen_at, absolute_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, digest(token), session.User, session.CSRF, session.AuthVersion, session.CLI, session.Created, session.Seen, session.Absolute); err != nil {
		return false, err
	}
	if err = insertAudits(ctx, tx, audits); err != nil {
		return false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) logoutSessionAudited(ctx context.Context, sessionHash, browserHash string, audits []Audit) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if sessionHash != "" {
		if _, err = tx.Exec(ctx, `DELETE FROM krun_sessions WHERE session_hash=$1`, sessionHash); err != nil {
			return err
		}
	}
	if browserHash != "" {
		if _, err = tx.Exec(ctx, `UPDATE krun_browsers SET generation=generation+1 WHERE browser_hash=$1`, browserHash); err != nil {
			return err
		}
	}
	if err = insertAudits(ctx, tx, audits); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) upsertRepositoryConnectState(ctx context.Context, state string, pending WorkerRepositoryConnectState) error {
	if s.pool == nil {
		if st := s.memState(); st != nil {
			st.RepositoryConnectStates[state] = pending
		}
		return nil
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO krun_repository_connect_states (state, user_id, provider_id, org_id, browser_hash, expires_at) VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT (state) DO UPDATE SET user_id=excluded.user_id, provider_id=excluded.provider_id, org_id=excluded.org_id, browser_hash=excluded.browser_hash, expires_at=excluded.expires_at`, state, pending.User, pending.Provider, pending.Organization, pending.Browser, pending.Expires)
	return err
}

// consumeRepositoryConnectState is the one-shot CSRF boundary for repository
// OAuth. PostgreSQL validates the browser binding and expiry in the same
// DELETE ... RETURNING statement, so two callbacks cannot both exchange a
// code. The memory branch is called from Store.Update and is protected by the
// store mutex.
func (s *Store) consumeRepositoryConnectState(ctx context.Context, state, browser string) (WorkerRepositoryConnectState, bool, error) {
	if s.pool == nil {
		st := s.memState()
		pending, ok := st.RepositoryConnectStates[state]
		if !ok || pending.Browser != browser || !now().Before(pending.Expires) {
			return WorkerRepositoryConnectState{}, false, nil
		}
		delete(st.RepositoryConnectStates, state)
		return pending, true, nil
	}
	var out WorkerRepositoryConnectState
	err := s.pool.QueryRow(ctx, `DELETE FROM krun_repository_connect_states
        WHERE state=$1 AND browser_hash=$2 AND expires_at > now()
        RETURNING user_id, provider_id, org_id, browser_hash, expires_at`, state, browser).
		Scan(&out.User, &out.Provider, &out.Organization, &out.Browser, &out.Expires)
	if errors.Is(err, pgx.ErrNoRows) {
		return WorkerRepositoryConnectState{}, false, nil
	}
	if err != nil {
		return WorkerRepositoryConnectState{}, false, err
	}
	return out, true, nil
}

func (s *Store) upsertRepositoryConnection(ctx context.Context, connection WorkerRepositoryConnection) error {
	if s.pool == nil {
		if st := s.memState(); st != nil {
			key := connection.User + "\x00" + connection.Provider
			if previous, ok := st.RepositoryConnections[key]; ok {
				connection.TokenRevision = previous.TokenRevision + 1
			} else {
				connection.TokenRevision = 1
			}
			connection.RefreshState = "ready"
			connection.RefreshClaimedAt = time.Time{}
			connection.ReconnectRequired = false
			st.RepositoryConnections[key] = connection
		}
		return nil
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO krun_repository_connections
		(user_id, provider_id, access_token, token_type, refresh_token, expires_at, token_revision,
		 refresh_state, refresh_claimed_at, reconnect_required, connected_at)
		VALUES ($1,$2,$3,$4,$5,$6,1,'ready',NULL,false,$7)
		ON CONFLICT (user_id, provider_id) DO UPDATE SET
		access_token=excluded.access_token, token_type=excluded.token_type,
		refresh_token=excluded.refresh_token, expires_at=excluded.expires_at,
		token_revision=krun_repository_connections.token_revision+1,
		refresh_state='ready', refresh_claimed_at=NULL, reconnect_required=false,
		connected_at=excluded.connected_at`, connection.User, connection.Provider,
		connection.AccessToken, connection.TokenType, connection.RefreshToken,
		repositoryTokenExpiry(connection.ExpiresAt), connection.ConnectedAt)
	return err
}

func (s *Store) deleteRepositoryConnection(ctx context.Context, user, provider string) error {
	if s.pool == nil {
		if st := s.memState(); st != nil {
			delete(st.RepositoryConnections, user+"\x00"+provider)
		}
		return nil
	}
	_, err := s.pool.Exec(ctx, `DELETE FROM krun_repository_connections WHERE user_id=$1 AND provider_id=$2`, user, provider)
	return err
}

// repositoryConnection fetches one credential only at the point where a clone
// needs it. Normal Workers reads never load access tokens into the state graph.
func (s *Store) repositoryConnection(ctx context.Context, user, provider string) (WorkerRepositoryConnection, bool, error) {
	if s.pool == nil {
		connection, ok := s.memState().RepositoryConnections[user+"\x00"+provider]
		return connection, ok, nil
	}
	out, err := scanRepositoryConnection(s.pool.QueryRow(ctx, `SELECT access_token, token_type, refresh_token,
		expires_at, token_revision, refresh_state, refresh_claimed_at, reconnect_required, connected_at
		FROM krun_repository_connections WHERE user_id=$1 AND provider_id=$2`, user, provider), user, provider)
	if errors.Is(err, pgx.ErrNoRows) {
		return WorkerRepositoryConnection{}, false, nil
	}
	if err != nil {
		return WorkerRepositoryConnection{}, false, err
	}
	return out, true, nil
}

type repositoryConnectionScanner interface {
	Scan(dest ...any) error
}

func scanRepositoryConnection(row repositoryConnectionScanner, user, provider string) (WorkerRepositoryConnection, error) {
	var out WorkerRepositoryConnection
	var expires, claimed pgtype.Timestamptz
	err := row.Scan(&out.AccessToken, &out.TokenType, &out.RefreshToken, &expires,
		&out.TokenRevision, &out.RefreshState, &claimed, &out.ReconnectRequired, &out.ConnectedAt)
	if err != nil {
		return WorkerRepositoryConnection{}, err
	}
	out.User, out.Provider = user, provider
	if expires.Valid {
		out.ExpiresAt = expires.Time
	}
	if claimed.Valid {
		out.RefreshClaimedAt = claimed.Time
	}
	return out, nil
}

func repositoryTokenExpiry(expires time.Time) any {
	if expires.IsZero() {
		return nil
	}
	return expires
}

// claimRepositoryConnectionRefresh leases one rotating refresh token without
// keeping a transaction open during the provider call. token_revision is the
// fencing token; a crashed caller can be replaced after the short lease.
func (s *Store) claimRepositoryConnectionRefresh(ctx context.Context, user, provider string, expected int64, at time.Time) (WorkerRepositoryConnection, bool, error) {
	if s.pool == nil {
		key := user + "\x00" + provider
		connection, ok := s.memState().RepositoryConnections[key]
		if !ok {
			return WorkerRepositoryConnection{}, false, nil
		}
		if connection.TokenRevision == 0 {
			connection.TokenRevision = 1
		}
		leaseExpired := connection.RefreshState == "refreshing" && connection.RefreshClaimedAt.Before(at.Add(-time.Minute))
		if connection.TokenRevision != expected || connection.ReconnectRequired || (connection.RefreshState != "" && connection.RefreshState != "ready" && !leaseExpired) {
			return connection, false, nil
		}
		connection.TokenRevision++
		connection.RefreshState = "refreshing"
		connection.RefreshClaimedAt = at
		s.memState().RepositoryConnections[key] = connection
		return connection, true, nil
	}
	out, err := scanRepositoryConnection(s.pool.QueryRow(ctx, `UPDATE krun_repository_connections SET
		token_revision=token_revision+1, refresh_state='refreshing', refresh_claimed_at=now()
		WHERE user_id=$1 AND provider_id=$2 AND token_revision=$3 AND reconnect_required=false
		  AND (refresh_state='ready' OR refresh_claimed_at < now() - interval '1 minute')
		RETURNING access_token, token_type, refresh_token, expires_at, token_revision,
			refresh_state, refresh_claimed_at, reconnect_required, connected_at`,
		user, provider, expected), user, provider)
	if errors.Is(err, pgx.ErrNoRows) {
		return WorkerRepositoryConnection{}, false, nil
	}
	return out, err == nil, err
}

func (s *Store) completeRepositoryConnectionRefresh(ctx context.Context, connection WorkerRepositoryConnection, claimRevision int64) (bool, error) {
	if s.pool == nil {
		key := connection.User + "\x00" + connection.Provider
		current, ok := s.memState().RepositoryConnections[key]
		if !ok || current.TokenRevision != claimRevision || current.RefreshState != "refreshing" {
			return false, nil
		}
		connection.TokenRevision = claimRevision + 1
		connection.RefreshState = "ready"
		connection.RefreshClaimedAt = time.Time{}
		connection.ReconnectRequired = false
		s.memState().RepositoryConnections[key] = connection
		return true, nil
	}
	tag, err := s.pool.Exec(ctx, `UPDATE krun_repository_connections SET
		access_token=$4, token_type=$5, refresh_token=$6, expires_at=$7,
		token_revision=token_revision+1, refresh_state='ready', refresh_claimed_at=NULL,
		reconnect_required=false
		WHERE user_id=$1 AND provider_id=$2 AND token_revision=$3 AND refresh_state='refreshing'`,
		connection.User, connection.Provider, claimRevision, connection.AccessToken,
		connection.TokenType, connection.RefreshToken, repositoryTokenExpiry(connection.ExpiresAt))
	return tag.RowsAffected() == 1, err
}

func (s *Store) failRepositoryConnectionRefresh(ctx context.Context, user, provider string, claimRevision int64, reconnect bool) (bool, error) {
	if s.pool == nil {
		key := user + "\x00" + provider
		connection, ok := s.memState().RepositoryConnections[key]
		if !ok || connection.TokenRevision != claimRevision || connection.RefreshState != "refreshing" {
			return false, nil
		}
		connection.TokenRevision++
		connection.RefreshState = "ready"
		connection.RefreshClaimedAt = time.Time{}
		connection.ReconnectRequired = reconnect
		if reconnect {
			connection.AccessToken, connection.RefreshToken = "", ""
		}
		s.memState().RepositoryConnections[key] = connection
		return true, nil
	}
	tag, err := s.pool.Exec(ctx, `UPDATE krun_repository_connections SET
		token_revision=token_revision+1, refresh_state='ready', refresh_claimed_at=NULL,
		reconnect_required=$4,
		access_token=CASE WHEN $4 THEN '' ELSE access_token END,
		refresh_token=CASE WHEN $4 THEN '' ELSE refresh_token END
		WHERE user_id=$1 AND provider_id=$2 AND token_revision=$3 AND refresh_state='refreshing'`,
		user, provider, claimRevision, reconnect)
	return tag.RowsAffected() == 1, err
}

// connectedRepositoryProviders deliberately selects only provider IDs: the
// provider-list endpoint must not materialize access tokens just to show a
// boolean badge.
func (s *Store) connectedRepositoryProviders(ctx context.Context, user string) (map[string]bool, error) {
	out := map[string]bool{}
	if s.pool == nil {
		for _, connection := range s.memState().RepositoryConnections {
			refreshable := connection.ExpiresAt.IsZero() || connection.ExpiresAt.After(now()) || connection.RefreshToken != ""
			if connection.User == user && connection.AccessToken != "" && !connection.ReconnectRequired && refreshable {
				out[connection.Provider] = true
			}
		}
		return out, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT provider_id FROM krun_repository_connections
		WHERE user_id=$1 AND access_token <> '' AND reconnect_required=false
		  AND (expires_at IS NULL OR expires_at > now() OR refresh_token <> '')`, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var provider string
		if err = rows.Scan(&provider); err != nil {
			return nil, err
		}
		out[provider] = true
	}
	return out, rows.Err()
}

func (s *Store) upsertRepositoryConnectStateAudited(ctx context.Context, state string, pending WorkerRepositoryConnectState, audits []Audit) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `INSERT INTO krun_repository_connect_states
		(state, user_id, provider_id, org_id, browser_hash, expires_at) VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (state) DO UPDATE SET user_id=excluded.user_id, provider_id=excluded.provider_id,
		org_id=excluded.org_id, browser_hash=excluded.browser_hash, expires_at=excluded.expires_at`,
		state, pending.User, pending.Provider, pending.Organization, pending.Browser, pending.Expires); err != nil {
		return err
	}
	if err = insertAudits(ctx, tx, audits); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) upsertRepositoryConnectionAudited(ctx context.Context, connection WorkerRepositoryConnection, audits []Audit) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `INSERT INTO krun_repository_connections
		(user_id, provider_id, access_token, token_type, refresh_token, expires_at, token_revision,
		 refresh_state, refresh_claimed_at, reconnect_required, connected_at)
		VALUES ($1,$2,$3,$4,$5,$6,1,'ready',NULL,false,$7)
		ON CONFLICT (user_id, provider_id) DO UPDATE SET
		access_token=excluded.access_token, token_type=excluded.token_type,
		refresh_token=excluded.refresh_token, expires_at=excluded.expires_at,
		token_revision=krun_repository_connections.token_revision+1,
		refresh_state='ready', refresh_claimed_at=NULL, reconnect_required=false,
		connected_at=excluded.connected_at`, connection.User, connection.Provider,
		connection.AccessToken, connection.TokenType, connection.RefreshToken,
		repositoryTokenExpiry(connection.ExpiresAt), connection.ConnectedAt); err != nil {
		return err
	}
	if err = insertAudits(ctx, tx, audits); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) deleteRepositoryConnectionAudited(ctx context.Context, user, provider string, audits []Audit) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `DELETE FROM krun_repository_connections WHERE user_id=$1 AND provider_id=$2`, user, provider); err != nil {
		return err
	}
	if err = insertAudits(ctx, tx, audits); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
