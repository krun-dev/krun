package storage

import (
	"context"
	_ "embed"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// schemaSQL is the full baseline for new databases. Existing databases may
// change baselines only through the explicit migration command.
//
// No foreign keys: reference integrity comes from write-path validation,
// explicit cascade deletes in the same statement batch, and reconciliation
// queries for orphans. See AGENTS.md section 9.
//
//go:embed schema.sql
var schemaSQL string

// An existing database must match the schema fingerprint before serving.
func initializeStoreSchema(ctx context.Context, pool *pgxpool.Pool) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT to_regclass(current_schema() || '.krun_meta') IS NOT NULL`).Scan(&exists); err != nil {
		return err
	}
	baseline := bytesDigest([]byte(schemaSQL))
	if exists {
		var stored string
		err = tx.QueryRow(ctx, `SELECT value FROM krun_meta WHERE key='schema_baseline'`).Scan(&stored)
		if errors.Is(err, pgx.ErrNoRows) || err == nil && stored != baseline {
			return errors.New("database does not match the current schema baseline; run krun-server migrate for a supported upgrade or restore a matching database")
		}
		if err != nil {
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
	if _, err = tx.Exec(ctx, schemaSQL); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO krun_meta(key,value) VALUES('schema_baseline',$1)`, baseline); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
