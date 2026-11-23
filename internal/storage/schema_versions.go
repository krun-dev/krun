package storage

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"

	"github.com/jackc/pgx/v5"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

var migrationName = regexp.MustCompile(`^([0-9]{4})_[a-z0-9_]+\.sql$`)
var onlineMigrationName = regexp.MustCompile(`^([0-9]{4})_online_from_([0-9]{4})_[a-z0-9_]+\.sql$`)
var migrationChecksum = regexp.MustCompile(`^[0-9a-f]{64}$`)

type sqlMigration struct {
	version             int
	name, sql, checksum string
}

func sqlMigrations() ([]sqlMigration, error) {
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		return nil, err
	}
	var result []sqlMigration
	for _, entry := range entries {
		matches := migrationName.FindStringSubmatch(entry.Name())
		if entry.IsDir() || matches == nil {
			return nil, fmt.Errorf("invalid migration filename %q", entry.Name())
		}
		version, _ := strconv.Atoi(matches[1])
		data, err := migrationFiles.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return nil, err
		}
		result = append(result, sqlMigration{version, entry.Name(), string(data), bytesDigest(data)})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].version < result[j].version })
	for i, m := range result {
		if m.version != i+1 {
			return nil, fmt.Errorf("migration versions must be consecutive from 0001: %s", m.name)
		}
		// Every incremental migration declares its supported baseline.
		if m.version > 1 {
			match := onlineMigrationName.FindStringSubmatch(m.name)
			if match == nil {
				return nil, fmt.Errorf("migration must declare online compatibility: %s", m.name)
			}
			minimum, _ := strconv.Atoi(match[2])
			if minimum < 1 || minimum >= m.version {
				return nil, fmt.Errorf("invalid minimum online schema version: %s", m.name)
			}
		}
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("no schema migrations")
	}
	return result, nil
}

func createMigrationLedger(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `CREATE TABLE krun_schema_migrations (version integer PRIMARY KEY, name text NOT NULL, checksum text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now())`)
	return err
}

// validateMigrationLedger rejects missing, unknown or modified history. It returns
// the number of applied migrations; callers decide whether pending steps are allowed.
func validateMigrationLedger(ctx context.Context, tx pgx.Tx, migrations []sqlMigration) (int, error) {
	rows, err := tx.Query(ctx, `SELECT version, name, checksum FROM krun_schema_migrations ORDER BY version`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var version int
		var name, checksum string
		if err := rows.Scan(&version, &name, &checksum); err != nil {
			return 0, err
		}
		if count >= len(migrations) || version != migrations[count].version || name != migrations[count].name || checksum != migrations[count].checksum {
			return 0, fmt.Errorf("database migration history does not match this binary at version %d", version)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if count == 0 {
		return 0, fmt.Errorf("database migration history is empty")
	}
	return count, nil
}

func recordMigration(ctx context.Context, tx pgx.Tx, m sqlMigration) error {
	_, err := tx.Exec(ctx, `INSERT INTO krun_schema_migrations(version,name,checksum) VALUES ($1,$2,$3)`, m.version, m.name, m.checksum)
	return err
}

// applyPendingMigrations applies and records each step in the caller's transaction.
func applyPendingMigrations(ctx context.Context, tx pgx.Tx, migrations []sqlMigration) error {
	for _, m := range migrations {
		if _, err := tx.Exec(ctx, m.sql); err != nil {
			return fmt.Errorf("apply migration %s: %w", m.name, err)
		}
		if err := recordMigration(ctx, tx, m); err != nil {
			return fmt.Errorf("record migration %s: %w", m.name, err)
		}
	}
	return nil
}

// checkServingMigrationLedger accepts newer migrations only when each explicitly
// declares compatibility with this binary's schema version. Known history is
// still verified byte-for-byte. A marker is a release contract, not SQL analysis.
func checkServingMigrationLedger(ctx context.Context, tx pgx.Tx, migrations []sqlMigration) error {
	rows, err := tx.Query(ctx, `SELECT version, name, checksum FROM krun_schema_migrations ORDER BY version`)
	if err != nil {
		return err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var version int
		var name, checksum string
		if err := rows.Scan(&version, &name, &checksum); err != nil {
			return err
		}
		count++
		if version != count {
			return fmt.Errorf("database migration history does not match this binary at version %d", version)
		}
		if count <= len(migrations) {
			m := migrations[count-1]
			if version != m.version || name != m.name || checksum != m.checksum {
				return fmt.Errorf("database migration history does not match this binary at version %d", version)
			}
			continue
		}
		match := onlineMigrationName.FindStringSubmatch(name)
		if match == nil || !migrationChecksum.MatchString(checksum) {
			return fmt.Errorf("database migration history does not match this binary at version %d", version)
		}
		namedVersion, _ := strconv.Atoi(match[1])
		minimum, _ := strconv.Atoi(match[2])
		if namedVersion != version || minimum < 1 || minimum > len(migrations) || minimum >= version {
			return fmt.Errorf("database migration %s is not compatible with this binary", name)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if count < len(migrations) {
		return errors.New("database has pending schema migrations; apply the SQL migrations manually")
	}
	return nil
}

// checkCurrentSchema verifies the SQL history, or the fingerprint of a legacy database.
func checkCurrentSchema(ctx context.Context, tx pgx.Tx, migrations []sqlMigration) error {
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT to_regclass(format('%I.krun_schema_migrations', current_schema())) IS NOT NULL`).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return errors.New("database migration history is missing; initialize a new database")
	}
	return checkServingMigrationLedger(ctx, tx, migrations)
}
