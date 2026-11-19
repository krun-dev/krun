package storage

import (
	"context"
	_ "embed"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"
)

// schemaSQL is the current schema snapshot used to generate and verify models.
// New databases are built from the immutable SQL migration chain.
//
// No foreign keys: reference integrity comes from write-path validation,
// explicit cascade deletes in the same statement batch, and reconciliation
// queries for orphans. See AGENTS.md section 9.
//
//go:embed schema.sql
var schemaSQL string

// Existing databases must have the complete expected migration history before serving.
func initializeStoreSchema(ctx context.Context, pool *pgxpool.Pool) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	schema, err := scopeMaintenanceSchema(ctx, tx)
	if err != nil {
		return err
	}
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT to_regclass(format('%I.krun_meta', $1::text)) IS NOT NULL`, schema).Scan(&exists); err != nil {
		return err
	}
	migrations, err := sqlMigrations()
	if err != nil {
		return err
	}
	if exists {
		if err := checkCurrentSchema(ctx, tx, migrations); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	var occupied bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_tables WHERE schemaname=current_schema() AND (tablename LIKE 'krun_%' OR tablename LIKE 'worker_%'))`).Scan(&occupied); err != nil {
		return err
	}
	if occupied {
		return errors.New("application tables exist without a schema baseline; initialize an empty development/test database explicitly")
	}
	if _, err = tx.Exec(ctx, migrations[0].sql); err != nil {
		return err
	}
	if err = createMigrationLedger(ctx, tx); err != nil {
		return err
	}
	if err = recordMigration(ctx, tx, migrations[0]); err != nil {
		return err
	}
	if err = applyPendingMigrations(ctx, tx, migrations[1:]); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
