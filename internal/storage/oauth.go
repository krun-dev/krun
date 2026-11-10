package storage

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/storage/models"
)

var ErrOAuthGrant = errors.New("invalid OAuth grant")

func oauthAuthorizationRow(a domain.OAuthAuthorization) models.OauthAuthorization {
	row := models.OauthAuthorization{AuthHash: a.Hash, ClientID: a.Client, RedirectUri: a.Redirect, Challenge: a.Challenge, State: a.State, Scope: a.Scope, Decision: a.Decision, UserID: a.User, AuthVersion: a.AuthVersion, CreatedAt: a.Created, ExpiresAt: a.Expires, Consumed: a.Consumed}
	if a.UserCode != "" {
		row.UserCode = &a.UserCode
	}
	if a.CodeHash != "" {
		row.CodeHash = &a.CodeHash
	}
	return row
}
func oauthAuthorization(row models.OauthAuthorization) domain.OAuthAuthorization {
	a := domain.OAuthAuthorization{Hash: row.AuthHash, Client: row.ClientID, Redirect: row.RedirectUri, Challenge: row.Challenge, State: row.State, Scope: row.Scope, Decision: row.Decision, User: row.UserID, AuthVersion: row.AuthVersion, Created: row.CreatedAt, Expires: row.ExpiresAt, Consumed: row.Consumed}
	if row.UserCode != nil {
		a.UserCode = *row.UserCode
	}
	if row.CodeHash != nil {
		a.CodeHash = *row.CodeHash
	}
	return a
}
func (s *Store) InsertOAuthAuthorization(ctx context.Context, a domain.OAuthAuthorization) error {
	row := oauthAuthorizationRow(a)
	_, err := s.orm.NewInsert().Model(&row).Returning("NULL").Exec(ctx)
	return err
}

// Lookup keys are fixed by the caller; no database objects leave storage.
func (s *Store) OAuthAuthorization(ctx context.Context, kind, value string) (domain.OAuthAuthorization, bool, error) {
	column := "auth_hash"
	switch kind {
	case "hash":
	case "code":
		column = "code_hash"
	case "user":
		column = "user_code"
	default:
		return domain.OAuthAuthorization{}, false, ErrOAuthGrant
	}
	var row models.OauthAuthorization
	err := s.orm.NewSelect().Model(&row).Where(column+" = ?", value).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.OAuthAuthorization{}, false, nil
	}
	return oauthAuthorization(row), err == nil, err
}
func (s *Store) DecideOAuthAuthorization(ctx context.Context, a domain.OAuthAuthorization, audits []domain.Audit) error {
	tx, err := s.beginORMTransaction(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	row := oauthAuthorizationRow(a)
	result, err := tx.orm.NewUpdate().Model(&row).Column("decision", "user_id", "auth_version", "code_hash").WherePK().Where("decision = 'pending'").Where("consumed = false").Where("expires_at > now()").Exec(ctx)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrOAuthGrant
	}
	if err = insertAudits(ctx, tx, audits); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Store) OAuthUser(ctx context.Context, id string) (domain.User, bool, error) {
	var r models.User
	err := s.orm.NewSelect().Model(&r).Where("user_id = ?", id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.User{}, false, nil
	}
	return domain.User{ID: r.UserID, Source: r.Source, Subject: r.Subject, Name: r.Name, Email: r.Email, Status: r.Status, AuthVersion: r.AuthVersion}, err == nil, err
}
func (s *Store) OAuthRefreshGrant(ctx context.Context, hash string) (domain.OAuthRefresh, domain.OAuthGrant, bool, error) {
	var r models.OauthRefreshToken
	err := s.orm.NewSelect().Model(&r).Where("token_hash = ?", hash).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.OAuthRefresh{}, domain.OAuthGrant{}, false, nil
	}
	if err != nil {
		return domain.OAuthRefresh{}, domain.OAuthGrant{}, false, err
	}
	var g models.OauthGrant
	err = s.orm.NewSelect().Model(&g).Where("grant_id = ?", r.GrantID).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.OAuthRefresh{}, domain.OAuthGrant{}, false, nil
	}
	return domain.OAuthRefresh{Hash: r.TokenHash, Grant: r.GrantID, Created: r.CreatedAt, Expires: r.ExpiresAt, Consumed: r.Consumed}, domain.OAuthGrant{ID: g.GrantID, User: g.UserID, Client: g.ClientID, Scope: g.Scope, AuthVersion: g.AuthVersion, Created: g.CreatedAt, Expires: g.ExpiresAt, Revoked: g.Revoked}, err == nil, err
}

// ExchangeOAuth atomically consumes one code/refresh and inserts its replacement
// credentials. Each CAS loser issues nothing. Network calls happen elsewhere.
func (s *Store) ExchangeOAuth(ctx context.Context, authorizationHash, refreshHash string, g domain.OAuthGrant, r domain.OAuthRefresh, session domain.Session) error {
	tx, err := s.beginORMTransaction(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if authorizationHash != "" {
		result, e := tx.orm.NewUpdate().Model((*models.OauthAuthorization)(nil)).Set("consumed = true").Where("auth_hash = ?", authorizationHash).Where("decision = 'approved' AND consumed = false AND expires_at > now()").Exec(ctx)
		if e != nil {
			return e
		}
		n, e := result.RowsAffected()
		if e != nil {
			return e
		}
		if n != 1 {
			return ErrOAuthGrant
		}
		row := models.OauthGrant{GrantID: g.ID, UserID: g.User, ClientID: g.Client, Scope: g.Scope, AuthVersion: g.AuthVersion, CreatedAt: g.Created, ExpiresAt: g.Expires}
		if _, err = tx.orm.NewInsert().Model(&row).Returning("NULL").Exec(ctx); err != nil {
			return err
		}
	} else {
		// The grant row arbitrates rotation against revoke without a read lock.
		result, e := tx.orm.NewUpdate().Model((*models.OauthGrant)(nil)).Set("revoked = false").Where("grant_id = ?", g.ID).Where("revoked = false AND expires_at > now()").Exec(ctx)
		if e != nil {
			return e
		}
		n, e := result.RowsAffected()
		if e != nil {
			return e
		}
		if n != 1 {
			return ErrOAuthGrant
		}
		result, e = tx.orm.NewUpdate().Model((*models.OauthRefreshToken)(nil)).Set("consumed = true").Where("token_hash = ?", refreshHash).Where("grant_id = ?", g.ID).Where("consumed = false AND expires_at > now()").Exec(ctx)
		if e != nil {
			return e
		}
		n, e = result.RowsAffected()
		if e != nil {
			return e
		}
		if n != 1 {
			return ErrOAuthGrant
		}
	}
	if r.Hash != "" {
		row := models.OauthRefreshToken{TokenHash: r.Hash, GrantID: r.Grant, CreatedAt: r.Created, ExpiresAt: r.Expires}
		if _, err = tx.orm.NewInsert().Model(&row).Returning("NULL").Exec(ctx); err != nil {
			return err
		}
	}
	row := sessionModel(session.Hash, session)
	if _, err = tx.orm.NewInsert().Model(&row).Returning("NULL").Exec(ctx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Store) RevokeOAuthGrant(ctx context.Context, id string) error {
	_, err := s.orm.NewUpdate().Model((*models.OauthGrant)(nil)).Set("revoked = true").Where("grant_id = ?", id).Exec(ctx)
	return err
}
func (s *Store) OAuthGrantForToken(ctx context.Context, hash, client string) (string, error) {
	var id string
	err := s.pool.QueryRow(ctx, `SELECT g.grant_id FROM krun_oauth_grants g WHERE g.client_id=$2 AND (g.grant_id IN(SELECT grant_id FROM krun_oauth_refresh_tokens WHERE token_hash=$1) OR g.grant_id IN(SELECT oauth_grant_id FROM krun_sessions WHERE session_hash=$1)) LIMIT 1`, hash, client).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return id, err
}
