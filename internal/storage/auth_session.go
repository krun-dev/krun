package storage

import (
	"context"
	sql "database/sql"
	"errors"
	"time"

	models "github.com/krun-dev/krun/internal/storage/models"

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

	var row models.APIKeyHash
	err := s.orm.NewSelect().Model(&row).Column("key_id").Where("hash = ?", hash).Limit(1).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return row.KeyID, err == nil, err
}

func (s *Store) SessionByHash(ctx context.Context, hash string) (domain.Session, bool, error) {
	if hash == "" {
		return domain.Session{}, false, nil
	}

	var row models.Session
	err := s.orm.NewSelect().Model(&row).Where("session_hash = ?", hash).Where("oauth_grant_id = '' OR EXISTS(SELECT 1 FROM krun_oauth_grants g WHERE g.grant_id=krun_sessions.oauth_grant_id AND g.revoked=false AND g.expires_at>now())").Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Session{}, false, nil
	}
	if err != nil {
		return domain.Session{}, false, err
	}
	return domain.Session{Hash: row.SessionHash, OAuthGrant: row.OauthGrantID, OAuthScope: row.OauthScope, User: row.UserID, CSRF: row.CSRF, AuthVersion: row.AuthVersion, CLI: row.CLI, Created: row.CreatedAt, Seen: row.SeenAt, Absolute: row.AbsoluteAt}, true, nil
}

// touchSession refreshes seen_at only when the stored value is older than the
// throttle. It deliberately does not fail the request when it loses a race.
func (s *Store) TouchSession(ctx context.Context, hash string) error {
	_, err := s.orm.NewUpdate().Model((*models.Session)(nil)).Set("seen_at = now()").Where("session_hash = ?", hash).Where("seen_at < now() - ?::interval", seenThrottle.String()).Exec(ctx)
	return err
}

func (s *Store) InsertSession(ctx context.Context, token string, session domain.Session) error {
	row := sessionModel(digest(token), session)
	_, err := s.orm.NewInsert().Model(&row).Returning("NULL").Exec(ctx)
	return err
}

// browserFor reads one browser row. A missing row is not an error: the caller
// decides whether to issue a new browser cookie.
func (s *Store) BrowserFor(ctx context.Context, hash string) (domain.Browser, bool, error) {
	if hash == "" {
		return domain.Browser{}, false, nil
	}

	var row models.Browser
	err := s.orm.NewSelect().Model(&row).Where("browser_hash = ?", hash).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Browser{}, false, nil
	}
	if err != nil {
		return domain.Browser{}, false, err
	}
	return domain.Browser{CSRF: row.CSRF, Generation: row.Generation, Expires: row.ExpiresAt}, true, nil
}

func (s *Store) UpsertBrowser(ctx context.Context, hash string, browser domain.Browser) error {
	row := models.Browser{BrowserHash: hash, CSRF: browser.CSRF, Generation: browser.Generation, ExpiresAt: browser.Expires}
	_, err := s.orm.NewInsert().Model(&row).On("CONFLICT (browser_hash) DO UPDATE").Set("csrf = EXCLUDED.csrf").Set("generation = EXCLUDED.generation").Set("expires_at = EXCLUDED.expires_at").Returning("NULL").Exec(ctx)
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
