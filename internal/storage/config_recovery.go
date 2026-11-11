package storage

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// RotateConfigEpoch prepares a restored database for serving. Control planes
// and runtimes must be stopped before the restored database is opened.
func RotateConfigEpoch(ctx context.Context, dsn string) (string, error) {
	pool, err := openPostgresPool(ctx, dsn)
	if err != nil {
		return "", err
	}
	defer pool.Close()
	tx, err := pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	if _, err := scopeMaintenanceSchema(ctx, tx); err != nil {
		return "", err
	}
	var epoch string
	err = tx.QueryRow(ctx, `UPDATE krun_meta SET value=replace(gen_random_uuid()::text,'-','')
		WHERE key='worker_config_epoch' AND EXISTS (
			SELECT 1 FROM krun_meta WHERE key='schema_baseline' AND value=$1
		) RETURNING value`, bytesDigest([]byte(schemaSQL))).Scan(&epoch)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", errors.New("runtime recovery requires the current schema baseline and configuration epoch")
	}
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM worker_runtime_acks;
		UPDATE worker_config_checkpoints SET published_revision=0 WHERE published_revision<>0;
		UPDATE krun_worker_operations SET edge_revision=0,edge_pool='' WHERE state='committed' AND edge_revision<>0`); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return epoch, nil
}
