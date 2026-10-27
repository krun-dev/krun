package control

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// migrateWorkerActiveBuildCount is kept separate so the migration ledger can
// append it after the concurrently-developed config/source migrations settle.
func migrateWorkerActiveBuildCount(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `
ALTER TABLE krun_worker_services
	ADD COLUMN IF NOT EXISTS active_build_count integer NOT NULL DEFAULT 0;

UPDATE krun_worker_services service
SET active_build_count = (
	SELECT count(*) FROM krun_worker_builds build
	WHERE build.service_id=service.service_id
	  AND build.state NOT IN ('succeeded','failed')
);

ALTER TABLE krun_worker_services
	DROP CONSTRAINT IF EXISTS krun_worker_services_active_build_count_check;
ALTER TABLE krun_worker_services
	ADD CONSTRAINT krun_worker_services_active_build_count_check
	CHECK (active_build_count >= 0);
`)
	return err
}
