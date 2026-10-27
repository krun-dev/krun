package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// importLegacySnapshot moves a pre-split krun_control_state document into the
// normalized tables exactly once, then renames the old table out of the way.
//
// It refuses to start when the import fails: running with a half-populated
// control plane would be worse than not starting at all. The legacy table is
// kept (renamed) so a rollback has something to read.
func (s *Store) importLegacySnapshot(ctx context.Context) error {
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = 'krun_control_state')`).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return nil
	}
	// Any row in the new tables means the import already ran; the rename below
	// makes this idempotent, but a crash between import and rename needs the
	// same guard.
	var users int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM krun_users`).Scan(&users); err != nil {
		return err
	}
	if users > 0 {
		return s.archiveLegacyTable(ctx)
	}

	var raw []byte
	err := s.pool.QueryRow(ctx, `SELECT data FROM krun_control_state WHERE id=1`).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return s.archiveLegacyTable(ctx)
	}
	if err != nil {
		return err
	}
	if len(raw) == 0 {
		return s.archiveLegacyTable(ctx)
	}
	legacy := newState()
	if err := json.Unmarshal(raw, legacy); err != nil {
		return fmt.Errorf("legacy control state is unreadable: %w", err)
	}
	if legacy.Workers == nil {
		legacy.Workers = newWorkerState()
	}
	empty := newState()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = writeIAMDiff(ctx, tx, empty, legacy); err != nil {
		return fmt.Errorf("legacy IAM import failed: %w", err)
	}
	if err = writeWorkersDiff(ctx, tx, empty, legacy); err != nil {
		return fmt.Errorf("legacy Workers import failed: %w", err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO krun_meta (key, value) VALUES ('bootstrapped',$1) ON CONFLICT (key) DO UPDATE SET value=excluded.value`, boolValue(legacy.Bootstrapped)); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO krun_meta (key, value) VALUES ('revision',$1) ON CONFLICT (key) DO UPDATE SET value=excluded.value`, itoa(legacy.Revision)); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	return s.archiveLegacyTable(ctx)
}

func boolValue(v bool) string {
	if v {
		return "1"
	}
	return "0"
}

// archiveLegacyTable renames the document table so it can no longer be written
// by accident but still exists for a rollback.
func (s *Store) archiveLegacyTable(ctx context.Context) error {
	var legacyExists, archivedExists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = 'krun_control_state')`).Scan(&legacyExists); err != nil {
		return err
	}
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = 'krun_control_state_legacy')`).Scan(&archivedExists); err != nil {
		return err
	}
	if !legacyExists {
		return nil
	}
	if archivedExists {
		// A previous run already archived one copy; drop the empty leftover so
		// the name is free again.
		if _, err := s.pool.Exec(ctx, `DROP TABLE krun_control_state`); err != nil {
			return err
		}
		return nil
	}
	_, err := s.pool.Exec(ctx, `ALTER TABLE krun_control_state RENAME TO krun_control_state_legacy`)
	return err
}
