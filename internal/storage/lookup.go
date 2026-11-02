package storage

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func (s *Store) Persistent() bool { return s.pool != nil }

func (s *Store) APIKeyOwner(ctx context.Context, key string) (string, bool, error) {
	if s.pool == nil {
		value, ok := s.memState().Keys[key]
		return value.Owner, ok, nil
	}
	var owner string
	err := s.pool.QueryRow(ctx, `SELECT owner_user_id FROM krun_api_keys WHERE key_id=$1`, key).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	return owner, err == nil, err
}

func ValidateDSN(dsn string) error { _, err := pgxpool.ParseConfig(dsn); return err }
