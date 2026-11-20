package storage

import (
	"context"
	"testing"

	"github.com/krun-dev/krun/internal/testpgx"
)

func TestInitialSQLMigrationMatchesLegacyBaseline(t *testing.T) {
	migrations, err := sqlMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if migrations[0].checksum != "82934894c84ba5a9ebef6ff99f2038684c8649d07d0bc79c30b27ea939d0f9a1" {
		t.Fatal("the initial migration must remain identical to the last legacy baseline")
	}
}

func TestPostgresMigrationHistoryAndRollback(t *testing.T) {
	ctx := context.Background()
	schema := testpgx.NewSchema(t, "krun_migration_history")
	store, err := Open(ctx, schema.DSN, RepositoryTokenKeys{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	migrations, err := sqlMigrations()
	if err != nil {
		t.Fatal(err)
	}
	second := sqlMigration{len(migrations) + 1, "test.sql", `CREATE TABLE migration_test (value text); INSERT INTO migration_test VALUES ('preserved')`, "test-checksum"}
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = applyPendingMigrations(ctx, tx, []sqlMigration{second, {version: len(migrations) + 2, name: "bad.sql", sql: `INVALID SQL`, checksum: "bad"}}); err == nil {
		t.Fatal("invalid SQL succeeded")
	}
	_ = tx.Rollback(ctx)
	var exists bool
	if err := store.pool.QueryRow(ctx, `SELECT to_regclass('migration_test') IS NOT NULL`).Scan(&exists); err != nil || exists {
		t.Fatalf("failed migration was not rolled back: %v %v", exists, err)
	}
	tx, err = store.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := applyPendingMigrations(ctx, tx, []sqlMigration{second}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if reopened, err := Open(ctx, schema.DSN, RepositoryTokenKeys{}); err == nil {
		reopened.Close()
		t.Fatal("unknown migration accepted by older binary")
	}
	tx, err = store.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count, err := validateMigrationLedger(ctx, tx, append(migrations, second)); err != nil || count != len(migrations)+1 {
		t.Fatalf("valid history: count=%d err=%v", count, err)
	}
	if err := checkCurrentSchema(ctx, tx, append(migrations, second)); err != nil {
		t.Fatal(err)
	}
	second.checksum = "modified"
	if _, err := validateMigrationLedger(ctx, tx, append(migrations, second)); err == nil {
		t.Fatal("modified migration accepted")
	}
	_ = tx.Rollback(ctx)
}

func TestPostgresRegisterLegacyBaseline(t *testing.T) {
	ctx := context.Background()
	schema := testpgx.NewSchema(t, "krun_migration_legacy")
	store, err := Open(ctx, schema.DSN, RepositoryTokenKeys{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.pool.Exec(ctx, `DROP TABLE krun_schema_migrations; DROP TABLE krun_worker_address_claims; DROP TABLE krun_worker_cleanup; INSERT INTO krun_meta(key,value) VALUES ('schema_baseline','82934894c84ba5a9ebef6ff99f2038684c8649d07d0bc79c30b27ea939d0f9a1'); INSERT INTO krun_users(user_id,source,subject,name,status) VALUES('legacy','local','legacy','Legacy','active')`); err != nil {
		t.Fatal(err)
	}
	if applied, err := MigrateSchema(ctx, schema.DSN); err != nil || !applied {
		t.Fatalf("register legacy: %v %v", applied, err)
	}
	if applied, err := MigrateSchema(ctx, schema.DSN); err != nil || applied {
		t.Fatalf("retry: %v %v", applied, err)
	}
	var count int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM krun_users WHERE user_id='legacy'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("legacy data: %d %v", count, err)
	}
}

func TestPostgresMigrationLedgerDoesNotRequireLegacyFingerprint(t *testing.T) {
	ctx := context.Background()
	schema := testpgx.NewSchema(t, "krun_migration_no_fingerprint")
	store, err := Open(ctx, schema.DSN, RepositoryTokenKeys{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.pool.Exec(ctx, `INSERT INTO krun_meta(key,value) VALUES ('schema_baseline','obsolete') ON CONFLICT(key) DO UPDATE SET value='obsolete'`); err != nil {
		t.Fatal(err)
	}
	if reopened, err := Open(ctx, schema.DSN, RepositoryTokenKeys{}); err != nil {
		t.Fatal(err)
	} else {
		reopened.Close()
	}
	if _, err := store.pool.Exec(ctx, `DELETE FROM krun_meta WHERE key='schema_baseline'`); err != nil {
		t.Fatal(err)
	}
	if applied, err := MigrateSchema(ctx, schema.DSN); err != nil || applied {
		t.Fatalf("current ledger without fingerprint: applied=%t err=%v", applied, err)
	}
	if reopened, err := Open(ctx, schema.DSN, RepositoryTokenKeys{}); err != nil {
		t.Fatal(err)
	} else {
		reopened.Close()
	}
}
