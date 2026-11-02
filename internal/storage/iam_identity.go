package storage

import (
	"context"
	"database/sql"
	"errors"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/storage/models"
)

// Source and provider bindings are permanent. An idempotent registration may
// observe an existing row, but it may never reassign its source or namespace.
func writeIdentitySources(ctx context.Context, tx ormTransaction, before, after *domain.State) error {
	for _, source := range sortedKeys(after.Sources) {
		fingerprint := after.Sources[source]
		if isProviderKey(source) {
			continue
		}
		if old, ok := before.Sources[source]; ok && old == fingerprint {
			continue
		}
		row := models.IdentitySource{Source: source, Fingerprint: fingerprint}
		result, err := tx.orm.NewInsert().Model(&row).On("CONFLICT (source) DO NOTHING").Returning("NULL").Exec(ctx)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected == 0 {
			if err := tx.orm.NewSelect().Model(&row).WherePK().Scan(ctx); err != nil {
				return err
			}
			if row.Fingerprint != fingerprint {
				return domain.Problem(409, "REVISION_CONFLICT", "Identity source cannot be reassigned")
			}
		}
	}
	for _, key := range sortedKeys(after.Sources) {
		value := after.Sources[key]
		provider, ok := providerIDFromKey(key)
		if !ok {
			continue
		}
		if old, exists := before.Sources[key]; exists && old == value {
			continue
		}
		namespace := ""
		for _, candidate := range sortedKeys(after.Sources) {
			if ns, ok := namespaceFromKey(candidate); ok && after.Sources[candidate] == value {
				namespace = ns
				break
			}
		}
		row := models.IdentityProvider{ProviderID: provider, Source: value, Namespace: namespace}
		result, err := tx.orm.NewInsert().Model(&row).On("CONFLICT DO NOTHING").Returning("NULL").Exec(ctx)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected == 0 {
			if err := tx.orm.NewSelect().Model(&row).WherePK().Scan(ctx); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return domain.Problem(409, "REVISION_CONFLICT", "Identity namespace already registered")
				}
				return err
			}
			if row.Source != value || row.Namespace != namespace {
				return domain.Problem(409, "REVISION_CONFLICT", "Identity provider cannot be reassigned")
			}
		}
	}
	for key := range before.Sources {
		if _, exists := after.Sources[key]; !exists {
			return domain.Problem(409, "REVISION_CONFLICT", "Identity source registrations are permanent")
		}
	}
	return nil
}
