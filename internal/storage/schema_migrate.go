package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

type schemaMigration struct {
	from string
	to   string
	sql  string
}

// Each step records the exact schema.sql digests from the two releases.
// Keep applied steps immutable and append new steps in release order.
var schemaMigrations = []schemaMigration{
	{
		from: "8f802b41ccf74f398f6c4c8ede9c480ad29b7e29ae95d7febda6046baa3842ef",
		to:   "791bb9ebf10e4652f69918635175574492833a5b55b53db9806f0a604b333a30",
		sql:  `ALTER TABLE krun_worker_build_private ADD COLUMN config_path text NOT NULL DEFAULT ''`,
	},
	{
		from: "791bb9ebf10e4652f69918635175574492833a5b55b53db9806f0a604b333a30",
		to:   "f5ddb02a599235f4f87c218e94dcac41afa1212d8f8634f025254e9ab3f80877",
		sql: `ALTER TABLE krun_worker_operations
			ADD COLUMN claim_generation bigint NOT NULL DEFAULT 0,
			ADD COLUMN edge_pool text NOT NULL DEFAULT '';
			UPDATE krun_worker_operations o SET state='superseded', error='Deployment target is no longer current', finished_at=now()
			WHERE o.state='committed' AND NOT EXISTS (
				SELECT 1 FROM krun_worker_environments e WHERE e.worker_id=o.worker_id AND e.env_name=o.env_name
					AND e.enabled AND e.version_id=o.version_id AND e.deployment_revision=o.deployment_revision
			);
			UPDATE krun_worker_operations SET edge_revision=0 WHERE state='committed' AND edge_revision<>0`,
	},
	{
		from: "f5ddb02a599235f4f87c218e94dcac41afa1212d8f8634f025254e9ab3f80877",
		to:   "5ec33847b55dc6a84dd405672d22b70ff4c90f5ebd6597ea709cc57e9b13900e",
		sql: `INSERT INTO krun_meta(key,value) VALUES('worker_config_epoch',replace(gen_random_uuid()::text,'-',''));
			DELETE FROM worker_runtime_acks;
			ALTER TABLE worker_runtime_acks ADD COLUMN config_epoch text NOT NULL,
				DROP CONSTRAINT worker_runtime_acks_pkey, ADD PRIMARY KEY(pod_uid,config_epoch);
			UPDATE worker_config_checkpoints SET published_revision=0 WHERE published_revision<>0;
			UPDATE krun_worker_operations SET edge_revision=0,edge_pool='' WHERE state='committed' AND edge_revision<>0`,
	},
}

// MigrateSchema upgrades an existing database while the control plane is
// stopped. It reports whether this call applied changes.
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

	// The baseline row serializes explicit upgrades in this schema. An upgrade
	// waiting here sees the preceding transaction's committed fingerprint.
	var current string
	err = tx.QueryRow(ctx, `SELECT value FROM krun_meta WHERE key='schema_baseline' FOR UPDATE`).Scan(&current)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, errors.New("schema upgrade requires an existing schema fingerprint")
	}
	if err != nil {
		return false, fmt.Errorf("lock schema fingerprint: %w", err)
	}

	target := bytesDigest([]byte(schemaSQL))
	if current == target {
		return false, tx.Commit(ctx)
	}
	steps, err := planSchemaMigration(current, target)
	if err != nil {
		return false, err
	}
	for _, step := range steps {
		if _, err := tx.Exec(ctx, step.sql); err != nil {
			return false, fmt.Errorf("apply schema upgrade %s to %s: %w", step.from, step.to, err)
		}
	}
	tag, err := tx.Exec(ctx, `UPDATE krun_meta SET value=$1 WHERE key='schema_baseline' AND value=$2`, target, current)
	if err != nil {
		return false, fmt.Errorf("record schema fingerprint: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return false, errors.New("schema fingerprint did not advance")
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit schema upgrade: %w", err)
	}
	return true, nil
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

func planSchemaMigration(current, target string) ([]schemaMigration, error) {
	var steps []schemaMigration
	next := current
	for _, step := range schemaMigrations {
		if step.from != next {
			continue
		}
		steps = append(steps, step)
		next = step.to
		if next == target {
			return steps, nil
		}
	}
	return nil, fmt.Errorf("no supported schema upgrade from %q to %q", current, target)
}
