package storage

import (
	"context"
	sql "database/sql"
	"errors"
	models "github.com/krun-dev/krun/internal/storage/models"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/krun-dev/krun/internal/domain"
)

func (s *Store) UpsertRepositoryConnectState(ctx context.Context, state string, pending domain.WorkerRepositoryConnectState) error {
	if s.pool == nil {
		if st := s.memState(); st != nil {
			st.RepositoryConnectStates[state] = pending
		}
		return nil
	}
	_, err := repositoryConnectUpsert(s.orm, state, pending).Exec(ctx)
	return err
}

// consumeRepositoryConnectState is the one-shot CSRF boundary for repository
// OAuth. PostgreSQL validates the browser binding and expiry in the same
// DELETE ... RETURNING statement, so two callbacks cannot both exchange a
// code. The memory branch is called from Store.Update and is protected by the
// store mutex.
func (s *Store) ConsumeRepositoryConnectState(ctx context.Context, state, browser string) (domain.WorkerRepositoryConnectState, bool, error) {
	if s.pool == nil {
		st := s.memState()
		pending, ok := st.RepositoryConnectStates[state]
		if !ok || pending.Browser != browser || !now().Before(pending.Expires) {
			return domain.WorkerRepositoryConnectState{}, false, nil
		}
		delete(st.RepositoryConnectStates, state)
		return pending, true, nil
	}
	var out domain.WorkerRepositoryConnectState
	err := s.pool.QueryRow(ctx, `DELETE FROM krun_repository_connect_states
        WHERE state=$1 AND browser_hash=$2 AND expires_at > now()
        RETURNING user_id, provider_id, org_id, browser_hash, expires_at`, state, browser).
		Scan(&out.User, &out.Provider, &out.Organization, &out.Browser, &out.Expires)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.WorkerRepositoryConnectState{}, false, nil
	}
	if err != nil {
		return domain.WorkerRepositoryConnectState{}, false, err
	}
	return out, true, nil
}

func (s *Store) UpsertRepositoryConnection(ctx context.Context, connection domain.WorkerRepositoryConnection) error {
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
	_, err := repositoryConnectionUpsert(s.orm, connection).Exec(ctx)
	return err
}

func (s *Store) DeleteRepositoryConnection(ctx context.Context, user, provider string) error {
	if s.pool == nil {
		if st := s.memState(); st != nil {
			delete(st.RepositoryConnections, user+"\x00"+provider)
		}
		return nil
	}
	row := models.RepositoryConnection{UserID: user, ProviderID: provider}
	_, err := s.orm.NewDelete().Model(&row).WherePK().Exec(ctx)
	return err
}

// repositoryConnection fetches one credential only at the point where a clone
// needs it. Normal Workers reads never load access tokens into the state graph.
func (s *Store) RepositoryConnection(ctx context.Context, user, provider string) (domain.WorkerRepositoryConnection, bool, error) {
	if s.pool == nil {
		connection, ok := s.memState().RepositoryConnections[user+"\x00"+provider]
		return connection, ok, nil
	}
	var row models.RepositoryConnection
	err := s.orm.NewSelect().Model(&row).Where("user_id = ?", user).Where("provider_id = ?", provider).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.WorkerRepositoryConnection{}, false, nil
	}
	if err != nil {
		return domain.WorkerRepositoryConnection{}, false, err
	}
	out := domain.WorkerRepositoryConnection{User: row.UserID, Provider: row.ProviderID, AccessToken: row.AccessToken, TokenType: row.TokenType, RefreshToken: row.RefreshToken, TokenRevision: row.TokenRevision, RefreshState: row.RefreshState, ReconnectRequired: row.ReconnectRequired, ConnectedAt: row.ConnectedAt}
	if row.ExpiresAt != nil {
		out.ExpiresAt = *row.ExpiresAt
	}
	if row.RefreshClaimedAt != nil {
		out.RefreshClaimedAt = *row.RefreshClaimedAt
	}
	return out, true, nil
}

type repositoryConnectionScanner interface {
	Scan(dest ...any) error
}

func scanRepositoryConnection(row repositoryConnectionScanner, user, provider string) (domain.WorkerRepositoryConnection, error) {
	var out domain.WorkerRepositoryConnection
	var expires, claimed pgtype.Timestamptz
	err := row.Scan(&out.AccessToken, &out.TokenType, &out.RefreshToken, &expires,
		&out.TokenRevision, &out.RefreshState, &claimed, &out.ReconnectRequired, &out.ConnectedAt)
	if err != nil {
		return domain.WorkerRepositoryConnection{}, err
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
func (s *Store) ClaimRepositoryConnectionRefresh(ctx context.Context, user, provider string, expected int64, at time.Time) (domain.WorkerRepositoryConnection, bool, error) {
	if s.pool == nil {
		key := user + "\x00" + provider
		connection, ok := s.memState().RepositoryConnections[key]
		if !ok {
			return domain.WorkerRepositoryConnection{}, false, nil
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
		return domain.WorkerRepositoryConnection{}, false, nil
	}
	return out, err == nil, err
}

func (s *Store) CompleteRepositoryConnectionRefresh(ctx context.Context, connection domain.WorkerRepositoryConnection, claimRevision int64) (bool, error) {
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

func (s *Store) FailRepositoryConnectionRefresh(ctx context.Context, user, provider string, claimRevision int64, reconnect bool) (bool, error) {
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

// RepositoryConnectionStatuses projects only display state, never token values.
func (s *Store) RepositoryConnectionStatuses(ctx context.Context, user string) (map[string]string, error) {
	out := map[string]string{}
	if s.pool == nil {
		for _, connection := range s.memState().RepositoryConnections {
			if connection.User == user {
				out[connection.Provider] = connection.Status(now())
			}
		}
		return out, nil
	}
	var rows []struct {
		ProviderID string
		Status     string
	}
	err := s.orm.NewSelect().Model((*models.RepositoryConnection)(nil)).
		Column("provider_id").ColumnExpr(`CASE
            WHEN access_token = '' OR reconnect_required THEN 'reconnect_required'
            WHEN expires_at <= now() AND refresh_token = '' THEN 'reconnect_required'
            WHEN expires_at <= now() THEN 'refresh_due'
            ELSE 'connected' END AS status`).
		Where("user_id = ?", user).Scan(ctx, &rows)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[row.ProviderID] = row.Status
	}
	return out, nil
}

func (s *Store) ConnectedRepositoryProviders(ctx context.Context, user string) (map[string]bool, error) {
	statuses, err := s.RepositoryConnectionStatuses(ctx, user)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(statuses))
	for provider, status := range statuses {
		out[provider] = status == "connected" || status == "refresh_due"
	}
	return out, nil
}

// Reject only the grant used by the failed request. A concurrent refresh,
// disconnect or reconnect must not be overwritten.
func (s *Store) RejectRepositoryConnection(ctx context.Context, grant domain.WorkerRepositoryConnection) error {
	if s.pool == nil {
		key := grant.User + "\x00" + grant.Provider
		current, ok := s.memState().RepositoryConnections[key]
		if ok && current.TokenRevision == grant.TokenRevision && current.AccessToken == grant.AccessToken && current.RefreshState != "refreshing" {
			current.ReconnectRequired = true
			current.AccessToken = ""
			current.RefreshToken = ""
			current.TokenRevision++
			s.memState().RepositoryConnections[key] = current
		}
		return nil
	}
	_, err := s.orm.NewUpdate().Model((*models.RepositoryConnection)(nil)).
		Set("reconnect_required = true").Set("access_token = ''").Set("refresh_token = ''").
		Set("token_revision = token_revision + 1").
		Where("user_id = ? AND provider_id = ?", grant.User, grant.Provider).
		Where("token_revision = ? AND access_token = ? AND refresh_state <> 'refreshing'", grant.TokenRevision, grant.AccessToken).
		Exec(ctx)
	return err
}

func (s *Store) UpsertRepositoryConnectStateAudited(ctx context.Context, state string, pending domain.WorkerRepositoryConnectState, audits []domain.Audit) error {
	tx, err := s.beginORMTransaction(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = repositoryConnectUpsert(tx.orm, state, pending).Exec(ctx); err != nil {
		return err
	}

	if err = insertAudits(ctx, tx, audits); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) UpsertRepositoryConnectionAudited(ctx context.Context, connection domain.WorkerRepositoryConnection, audits []domain.Audit) error {
	tx, err := s.beginORMTransaction(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = repositoryConnectionUpsert(tx.orm, connection).Exec(ctx); err != nil {
		return err
	}

	if err = insertAudits(ctx, tx, audits); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) DeleteRepositoryConnectionAudited(ctx context.Context, user, provider string, audits []domain.Audit) error {
	tx, err := s.beginORMTransaction(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.orm.NewDelete().Model(&models.RepositoryConnection{UserID: user, ProviderID: provider}).WherePK().Exec(ctx); err != nil {
		return err
	}

	if err = insertAudits(ctx, tx, audits); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
