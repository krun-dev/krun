package storage

import (
	"context"
	"crypto/subtle"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/krun-dev/krun/internal/domain"
)

const seenThrottle = 30 * time.Second

// apiKeyIDByHash resolves only the presented credential. Key hashes are never
// loaded into the general IAM snapshot, which keeps every authenticated read
// from materializing all long-lived secrets in the process.
func (s *Store) APIKeyIDByHash(ctx context.Context, hash string) (string, bool, error) {
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

func (s *Store) SessionByHash(ctx context.Context, hash string) (domain.Session, bool, error) {
	if hash == "" {
		return domain.Session{}, false, nil
	}
	if s.pool == nil {
		session, ok := s.memState().Sessions[hash]
		return session, ok, nil
	}
	var out domain.Session
	err := s.pool.QueryRow(ctx, `SELECT session_hash, user_id, csrf, auth_version, cli, created_at, seen_at, absolute_at FROM krun_sessions WHERE session_hash=$1`, hash).Scan(&out.Hash, &out.User, &out.CSRF, &out.AuthVersion, &out.CLI, &out.Created, &out.Seen, &out.Absolute)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Session{}, false, nil
	}
	if err != nil {
		return domain.Session{}, false, err
	}
	return out, true, nil
}

// touchSession refreshes seen_at only when the stored value is older than the
// throttle. It deliberately does not fail the request when it loses a race.
func (s *Store) TouchSession(ctx context.Context, hash string) error {
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

func (s *Store) InsertSession(ctx context.Context, token string, session domain.Session) error {
	if s.pool == nil {
		if st := s.memState(); st != nil {
			st.Sessions[digest(token)] = session
		}
		return nil
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO krun_sessions (session_hash, user_id, csrf, auth_version, cli, created_at, seen_at, absolute_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, digest(token), session.User, session.CSRF, session.AuthVersion, session.CLI, session.Created, session.Seen, session.Absolute)
	return err
}

func (s *Store) DeleteSession(ctx context.Context, hash string) error {
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
func (s *Store) BrowserFor(ctx context.Context, hash string) (domain.Browser, bool, error) {
	if hash == "" {
		return domain.Browser{}, false, nil
	}
	if s.pool == nil {
		browser, ok := s.memState().Browsers[hash]
		return browser, ok, nil
	}
	var out domain.Browser
	err := s.pool.QueryRow(ctx, `SELECT csrf, generation, expires_at FROM krun_browsers WHERE browser_hash=$1`, hash).Scan(&out.CSRF, &out.Generation, &out.Expires)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Browser{}, false, nil
	}
	if err != nil {
		return domain.Browser{}, false, err
	}
	return out, true, nil
}

func (s *Store) UpsertBrowser(ctx context.Context, hash string, browser domain.Browser) error {
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

func (s *Store) LogoutSessionAudited(ctx context.Context, sessionHash, browserHash string, audits []domain.Audit) error {
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
