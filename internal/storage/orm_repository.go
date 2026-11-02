package storage

import (
	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/storage/models"
	"github.com/uptrace/bun"
	"time"
)

func repositoryConnectUpsert(db bun.IDB, state string, pending domain.WorkerRepositoryConnectState) *bun.InsertQuery {
	row := models.RepositoryConnectState{State: state, UserID: pending.User, ProviderID: pending.Provider, OrgID: pending.Organization, BrowserHash: pending.Browser, ExpiresAt: pending.Expires}
	return db.NewInsert().Model(&row).On("CONFLICT (state) DO UPDATE").Set("user_id = EXCLUDED.user_id").Set("provider_id = EXCLUDED.provider_id").Set("org_id = EXCLUDED.org_id").Set("browser_hash = EXCLUDED.browser_hash").Set("expires_at = EXCLUDED.expires_at").Returning("NULL")
}
func repositoryConnectionUpsert(db bun.IDB, connection domain.WorkerRepositoryConnection) *bun.InsertQuery {
	var expiry *time.Time
	if !connection.ExpiresAt.IsZero() {
		value := connection.ExpiresAt
		expiry = &value
	}
	row := models.RepositoryConnection{UserID: connection.User, ProviderID: connection.Provider, AccessToken: connection.AccessToken, TokenType: connection.TokenType, RefreshToken: connection.RefreshToken, ExpiresAt: expiry, TokenRevision: 1, RefreshState: "ready", ReconnectRequired: false, ConnectedAt: connection.ConnectedAt}
	return db.NewInsert().Model(&row).On("CONFLICT (user_id, provider_id) DO UPDATE").Set("access_token = EXCLUDED.access_token").Set("token_type = EXCLUDED.token_type").Set("refresh_token = EXCLUDED.refresh_token").Set("expires_at = EXCLUDED.expires_at").Set("token_revision = krun_repository_connections.token_revision + 1").Set("refresh_state = 'ready'").Set("refresh_claimed_at = NULL").Set("reconnect_required = false").Set("connected_at = EXCLUDED.connected_at").Returning("NULL")
}
