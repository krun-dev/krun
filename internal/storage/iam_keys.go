package storage

import (
	"context"
	"encoding/json"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/storage/models"
	"time"
)

// Metadata and the private authentication hash remain separate models/tables.
func writeAPIKeys(ctx context.Context, tx ormTransaction, before, after *domain.State) error {
	for id, key := range after.Keys {
		previous, existed := before.Keys[id]
		if existed && sameAPIKey(previous, key) {
			continue
		}
		policy, err := json.Marshal(key.Policy)
		if err != nil {
			return err
		}
		row := models.APIKey{KeyID: id, OwnerUserID: key.Owner, OrgID: key.Organization, ScopeKind: key.ScopeKind, Name: key.Name, Prefix: key.Prefix, Policy: policy, ExpiresAt: key.Expires, Revoked: key.Revoked, Revision: key.Revision}
		if !existed {
			if err := ormCAS(ctx, "Key changed", tx.orm.NewInsert().Model(&row).Column("key_id", "owner_user_id", "org_id", "scope_kind", "name", "prefix", "policy", "expires_at", "revoked", "revision").On("CONFLICT (key_id) DO NOTHING").Returning("NULL")); err != nil {
				return err
			}
		} else if err := ormCAS(ctx, "Key changed", tx.orm.NewUpdate().Model(&row).Column("owner_user_id", "org_id", "scope_kind", "name", "prefix", "policy", "expires_at", "revoked", "revision").WherePK().Where("revision = ?", previous.Revision)); err != nil {
			return err
		}
	}
	for id, previous := range before.Keys {
		if _, exists := after.Keys[id]; exists {
			continue
		}
		row := models.APIKey{KeyID: id}
		if err := ormCAS(ctx, "Key changed", tx.orm.NewDelete().Model(&row).WherePK().Where("revision = ?", previous.Revision)); err != nil {
			return err
		}
		hash := models.APIKeyHash{KeyID: id}
		if _, err := tx.orm.NewDelete().Model(&hash).WherePK().Exec(ctx); err != nil {
			return err
		}
	}
	for id, hash := range after.KeyHashes {
		if previous, exists := before.KeyHashes[id]; exists && previous == hash {
			continue
		}
		row := models.APIKeyHash{KeyID: id, Hash: hash}
		if _, err := tx.orm.NewInsert().Model(&row).On("CONFLICT (key_id) DO UPDATE").Set("hash = EXCLUDED.hash").Returning("NULL").Exec(ctx); err != nil {
			return err
		}
	}
	return nil
}
func writeInvitations(ctx context.Context, tx ormTransaction, before, after map[string]time.Time) error {
	for key, expiry := range after {
		if previous, exists := before[key]; exists && previous.Equal(expiry) {
			continue
		}
		source, subject := splitNulKey(key)
		row := models.Invitation{Source: source, Subject: subject, ExpiresAt: expiry}
		if _, err := tx.orm.NewInsert().Model(&row).On("CONFLICT (source, subject) DO UPDATE").Set("expires_at = EXCLUDED.expires_at").Returning("NULL").Exec(ctx); err != nil {
			return err
		}
	}
	for key := range before {
		if _, exists := after[key]; exists {
			continue
		}
		source, subject := splitNulKey(key)
		row := models.Invitation{Source: source, Subject: subject}
		if _, err := tx.orm.NewDelete().Model(&row).WherePK().Exec(ctx); err != nil {
			return err
		}
	}
	return nil
}
