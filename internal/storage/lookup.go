package storage

import (
	"context"
	sql "database/sql"
	"errors"

	models "github.com/krun-dev/krun/internal/storage/models"

	"github.com/jackc/pgx/v5/pgxpool"
)

func (s *Store) APIKeyOwner(ctx context.Context, key string) (string, bool, error) {
	var row models.APIKey
	err := s.orm.NewSelect().Model(&row).Column("owner_user_id").Where("key_id = ?", key).Limit(1).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return row.OwnerUserID, err == nil, err
}

func ValidateDSN(dsn string) error { _, err := pgxpool.ParseConfig(dsn); return err }
