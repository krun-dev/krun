package storage

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/krun-dev/krun/internal/testpgx"
)

func TestInitialSQLMigrationIsImmutable(t *testing.T) {
	migrations, err := sqlMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if migrations[0].checksum != "c1e65e3bcc0c5e35326319c43ae534e63b18fd0f74e91aefafdeba73118af78f" {
		t.Fatal("initial migration was modified")
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

func TestPostgresOnlineMigrationCompatibility(t *testing.T) {
	ctx := context.Background()
	schema := testpgx.NewSchema(t, "krun_online_compat")
	store, err := Open(ctx, schema.DSN, RepositoryTokenKeys{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	migrations, err := sqlMigrations()
	if err != nil {
		t.Fatal(err)
	}
	next := len(migrations) + 1
	name := fmt.Sprintf("%04d_online_from_%04d_add_example.sql", next, len(migrations))
	checksum := strings.Repeat("a", 64)
	if _, err := store.pool.Exec(ctx, `CREATE TABLE online_migration_example (value text)`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO krun_schema_migrations(version,name,checksum) VALUES($1,$2,$3)`, next, name, checksum); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, schema.DSN, RepositoryTokenKeys{})
	if err != nil {
		t.Fatalf("compatible newer schema: %v", err)
	}
	reopened.Close()
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	newVersion := append(append([]sqlMigration(nil), migrations...), sqlMigration{version: next, name: name, checksum: checksum})
	if err := checkCurrentSchema(ctx, tx, newVersion); err != nil {
		t.Fatalf("new version rejected migrated schema: %v", err)
	}
	_ = tx.Rollback(ctx)
	if _, err := store.pool.Exec(ctx, `INSERT INTO online_migration_example(value) VALUES('old instance still writing')`); err != nil {
		t.Fatal(err)
	}

	cases := []struct{ name, checksum string }{
		{fmt.Sprintf("%04d_online_from_%04d_breaking.sql", next, next), checksum},
		{fmt.Sprintf("%04d_unmarked.sql", next), checksum},
		{name, "bad-checksum"},
		{fmt.Sprintf("%04d_online_from_0001_wrong.sql", next+1), checksum},
	}
	for _, tc := range cases {
		if _, err := store.pool.Exec(ctx, `UPDATE krun_schema_migrations SET name=$1, checksum=$2 WHERE version=$3`, tc.name, tc.checksum, next); err != nil {
			t.Fatal(err)
		}
		if old, err := Open(ctx, schema.DSN, RepositoryTokenKeys{}); err == nil {
			old.Close()
			t.Fatalf("accepted incompatible migration %q", tc.name)
		}
	}
	if _, err := store.pool.Exec(ctx, `UPDATE krun_schema_migrations SET name=$1, checksum=$2 WHERE version=$3`, name, checksum, next); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `UPDATE krun_schema_migrations SET checksum='modified' WHERE version=1`); err != nil {
		t.Fatal(err)
	}
	if old, err := Open(ctx, schema.DSN, RepositoryTokenKeys{}); err == nil {
		old.Close()
		t.Fatal("accepted modified known migration")
	}
}

func TestPostgresMigrationLockDoesNotBlockMetadataWrites(t *testing.T) {
	ctx := context.Background()
	schema := testpgx.NewSchema(t, "krun_migration_lock")
	store, err := Open(ctx, schema.DSN, RepositoryTokenKeys{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `LOCK TABLE krun_schema_migrations IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	writeCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if _, err := store.pool.Exec(writeCtx, `INSERT INTO krun_meta(key,value) VALUES('online-test','ok')`); err != nil {
		t.Fatalf("metadata write blocked by migration lock: %v", err)
	}
}

func TestPostgresMigrateCurrentOrMissingLedger(t *testing.T) {
	ctx := context.Background()
	schema := testpgx.NewSchema(t, "krun_migration_explicit")
	store, err := Open(ctx, schema.DSN, RepositoryTokenKeys{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if applied, err := MigrateSchema(ctx, schema.DSN); err != nil || applied {
		t.Fatalf("current migration: applied=%v err=%v", applied, err)
	}
	if _, err := store.pool.Exec(ctx, `DROP TABLE krun_schema_migrations`); err != nil {
		t.Fatal(err)
	}
	if applied, err := MigrateSchema(ctx, schema.DSN); err == nil || applied {
		t.Fatalf("missing ledger accepted: applied=%v err=%v", applied, err)
	}
}
