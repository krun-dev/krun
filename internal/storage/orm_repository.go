package storage

import (
	"context"
	"time"

	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/storage/models"
	"github.com/uptrace/bun"
)

func repositoryConnectUpsert(db bun.IDB, state string, pending domain.WorkerRepositoryConnectState) *bun.InsertQuery {
	row := models.RepositoryConnectState{State: state, UserID: pending.User, ProviderID: pending.Provider, OrgID: pending.Organization, BrowserHash: pending.Browser, ExpiresAt: pending.Expires}
	return db.NewInsert().Model(&row).On("CONFLICT (state) DO UPDATE").Set("user_id = EXCLUDED.user_id").Set("provider_id = EXCLUDED.provider_id").Set("org_id = EXCLUDED.org_id").Set("browser_hash = EXCLUDED.browser_hash").Set("expires_at = EXCLUDED.expires_at").Returning("NULL")
}
func (s *Store) repositoryConnectionUpsert(ctx context.Context, db bun.IDB, connection domain.WorkerRepositoryConnection) error {
	access, refresh, err := s.repositoryTokenKeys.seal(connection)
	if err != nil {
		return err
	}

	var expiry *time.Time
	if !connection.ExpiresAt.IsZero() {
		value := connection.ExpiresAt
		expiry = &value
	}
	row := models.RepositoryConnection{
		UserID: connection.User, ProviderID: connection.Provider,
		AccessTokenCiphertext: access, RefreshTokenCiphertext: refresh,
		TokenKeyVersion: s.repositoryTokenKeys.Active, TokenType: connection.TokenType,
		ExpiresAt: expiry, RefreshState: "ready", ConnectedAt: connection.ConnectedAt,
	}
	_, err = db.NewInsert().Model(&row).
		Value("token_revision", "nextval('krun_repository_token_revision_seq')").
		On("CONFLICT (user_id, provider_id) DO UPDATE").
		Set("access_token_ciphertext = EXCLUDED.access_token_ciphertext").
		Set("refresh_token_ciphertext = EXCLUDED.refresh_token_ciphertext").
		Set("token_key_version = EXCLUDED.token_key_version").
		Set("token_type = EXCLUDED.token_type").
		Set("expires_at = EXCLUDED.expires_at").
		Set("token_revision = nextval('krun_repository_token_revision_seq')").
		Set("refresh_state = 'ready'").
		Set("refresh_claimed_at = NULL").
		Set("reconnect_required = false").
		Set("connected_at = EXCLUDED.connected_at").
		Returning("NULL").Exec(ctx)
	return err
}
