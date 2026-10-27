package control

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Sessions, browsers and login transactions are read and written with
// single-row statements. They are never part of an Update partition, so an
// authenticated request that only refreshes last-seen cost is one conditional
// UPDATE instead of a whole-state rewrite.
//
// seen_at is throttled: at most one write per session per seenThrottle. Without
// this, every authenticated request would be a write.

const seenThrottle = 30 * time.Second

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

func (s *Store) updateEmailLogin(ctx context.Context, hash string, login EmailLogin) error {
	if s.pool == nil {
		if st := s.memState(); st != nil {
			st.EmailLogins[hash] = login
		}
		return nil
	}
	_, err := s.pool.Exec(ctx, `UPDATE krun_email_logins SET attempts=$2, consumed=$3 WHERE challenge_hash=$1`, hash, login.Attempts, login.Consumed)
	return err
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
		if st := s.memState(); st != nil {
			st.Devices[device.CodeHash] = device
		}
		return nil
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO krun_device_codes (code_hash, user_code, token, expires_at, approved, consumed) VALUES ($1,$2,$3,$4,$5,$6)`, device.CodeHash, device.UserCode, device.Token, device.Expires, device.Approved, device.Consumed)
	return err
}

// consumeDevice clears the token and marks the code used in one statement, so
// two concurrent polls cannot both collect a CLI credential.
func (s *Store) consumeDevice(ctx context.Context, hash string) (string, bool, error) {
	if s.pool == nil {
		st := s.memState()
		device, ok := st.Devices[hash]
		if !ok || device.Consumed || !device.Approved {
			return "", false, nil
		}
		device.Consumed, device.Token = true, ""
		st.Devices[hash] = device
		return device.Token, true, nil
	}
	var token string
	err := s.pool.QueryRow(ctx, `UPDATE krun_device_codes SET consumed=true, token='' WHERE code_hash=$1 AND consumed=false AND approved=true RETURNING token`, hash).Scan(&token)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return token, true, nil
}

func (s *Store) approveDevice(ctx context.Context, hash, token string) error {
	if s.pool == nil {
		if st := s.memState(); st != nil {
			device := st.Devices[hash]
			device.Approved, device.Token = true, token
			st.Devices[hash] = device
		}
		return nil
	}
	_, err := s.pool.Exec(ctx, `UPDATE krun_device_codes SET approved=true, token=$2 WHERE code_hash=$1 AND consumed=false AND approved=false`, hash, token)
	return err
}

func (s *Store) repositoryConnectState(ctx context.Context, state string) (WorkerRepositoryConnectState, bool, error) {
	if s.pool == nil {
		pending, ok := s.memState().RepositoryConnectStates[state]
		return pending, ok, nil
	}
	var out WorkerRepositoryConnectState
	err := s.pool.QueryRow(ctx, `SELECT user_id, provider_id, browser_hash, expires_at FROM krun_repository_connect_states WHERE state=$1`, state).Scan(&out.User, &out.Provider, &out.Browser, &out.Expires)
	if errors.Is(err, pgx.ErrNoRows) {
		return WorkerRepositoryConnectState{}, false, nil
	}
	if err != nil {
		return WorkerRepositoryConnectState{}, false, err
	}
	return out, true, nil
}

func (s *Store) upsertRepositoryConnectState(ctx context.Context, state string, pending WorkerRepositoryConnectState) error {
	if s.pool == nil {
		if st := s.memState(); st != nil {
			st.RepositoryConnectStates[state] = pending
		}
		return nil
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO krun_repository_connect_states (state, user_id, provider_id, browser_hash, expires_at) VALUES ($1,$2,$3,$4,$5) ON CONFLICT (state) DO UPDATE SET user_id=excluded.user_id, provider_id=excluded.provider_id, browser_hash=excluded.browser_hash, expires_at=excluded.expires_at`, state, pending.User, pending.Provider, pending.Browser, pending.Expires)
	return err
}

func (s *Store) deleteRepositoryConnectState(ctx context.Context, state string) error {
	if s.pool == nil {
		if st := s.memState(); st != nil {
			delete(st.RepositoryConnectStates, state)
		}
		return nil
	}
	_, err := s.pool.Exec(ctx, `DELETE FROM krun_repository_connect_states WHERE state=$1`, state)
	return err
}

func (s *Store) upsertRepositoryConnection(ctx context.Context, connection WorkerRepositoryConnection) error {
	if s.pool == nil {
		if st := s.memState(); st != nil {
			st.RepositoryConnections[connection.User+"\x00"+connection.Provider] = connection
		}
		return nil
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO krun_repository_connections (user_id, provider_id, access_token, connected_at) VALUES ($1,$2,$3,$4) ON CONFLICT (user_id, provider_id) DO UPDATE SET access_token=excluded.access_token, connected_at=excluded.connected_at`, connection.User, connection.Provider, connection.AccessToken, connection.ConnectedAt)
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
