package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// MigrateSchema applies pending SQL migrations explicitly and reports changes.
// Online safety depends on the compatibility of the migration SQL.
func MigrateSchema(ctx context.Context, dsn string) (bool, error) {
	pool, err := openPostgresPool(ctx, dsn)
	if err != nil {
		return false, err
	}
	defer pool.Close()

	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout = '5s'`); err != nil {
		return false, err
	}

	schema, err := scopeMaintenanceSchema(ctx, tx)
	if err != nil {
		return false, err
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT to_regclass(format('%I.krun_meta', $1::text)) IS NOT NULL`, schema).Scan(&exists); err != nil {
		return false, err
	}
	if !exists {
		return false, errors.New("schema upgrade requires an existing krun database")
	}

	migrations, err := sqlMigrations()
	if err != nil {
		return false, err
	}
	var ledger bool
	if err := tx.QueryRow(ctx, `SELECT to_regclass(format('%I.krun_schema_migrations', $1::text)) IS NOT NULL`, schema).Scan(&ledger); err != nil {
		return false, err
	}
	if !ledger {
		return false, errors.New("schema migration history is missing; initialize a new database")
	}
	// Serialize migrations without blocking application metadata writes.
	if _, err := tx.Exec(ctx, `LOCK TABLE krun_schema_migrations IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return false, fmt.Errorf("lock schema migration: %w", err)
	}

	count, err := validateMigrationLedger(ctx, tx, migrations)
	if err != nil {
		return false, err
	}
	applied := false
	if count < len(migrations) {
		if err := applyPendingMigrations(ctx, tx, migrations[count:]); err != nil {
			return false, err
		}
		applied = true
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit schema upgrade: %w", err)
	}
	return applied, nil
}

func scopeMaintenanceSchema(ctx context.Context, tx pgx.Tx) (string, error) {
	var schema string
	if err := tx.QueryRow(ctx, `SELECT COALESCE(current_schema(), '')`).Scan(&schema); err != nil {
		return "", err
	}
	if schema == "" {
		return "", errors.New("PostgreSQL search_path has no existing schema")
	}
	// Keep maintenance statements in the selected schema.
	_, err := tx.Exec(ctx, `SELECT set_config('search_path', quote_ident($1) || ', pg_temp', true)`, schema)
	return schema, err
}
