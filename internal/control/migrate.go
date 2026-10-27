package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// importLegacySnapshot moves a pre-split krun_control_state document into the
// normalized tables exactly once, then renames the old table out of the way.
// If a rollback later recreates the fixed-name table after the import marker
// exists, startup fails closed so rollback-period writes cannot be discarded.
//
// It refuses to start when the import fails: running with a half-populated
// control plane would be worse than not starting at all. The legacy table is
// kept (renamed) so a rollback has something to read.
func (s *Store) importLegacySnapshot(ctx context.Context) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var exists bool
	if err = tx.QueryRow(ctx, `SELECT to_regclass(current_schema() || '.krun_control_state') IS NOT NULL`).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return nil
	}
	// DDL is transactional in PostgreSQL. Locking the fixed-name source table
	// serializes concurrent control-plane startups, and putting the import
	// marker plus archive rename in this same transaction removes the old
	// import-committed/rename-not-yet-run crash window.
	if _, err = tx.Exec(ctx, `LOCK TABLE krun_control_state IN ACCESS EXCLUSIVE MODE`); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "42P01" {
			// Another startup completed the transactional rename while this
			// transaction waited to acquire the relation lock.
			return nil
		}
		return err
	}
	// LOCK resolves a relation OID before waiting. If the winner renamed that
	// same OID while we waited, the lock may still succeed under its old parsed
	// name; resolve the public name again before touching it.
	if err = tx.QueryRow(ctx, `SELECT to_regclass(current_schema() || '.krun_control_state') IS NOT NULL`).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return nil
	}
	var imported bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM krun_meta WHERE key='legacy_imported' AND value='1')`).Scan(&imported); err != nil {
		return err
	}
	if imported {
		return errors.New("legacy control state reappeared after import; refusing to discard possible rollback writes")
	}

	var raw []byte
	err = tx.QueryRow(ctx, `SELECT data FROM krun_control_state WHERE id=1`).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		raw = nil
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	legacy := newState()
	if len(raw) > 0 {
		if err = json.Unmarshal(raw, legacy); err != nil {
			return fmt.Errorf("legacy control state is unreadable: %w", err)
		}
	}
	if legacy.Workers == nil {
		legacy.Workers = newWorkerState()
	}
	for id, organization := range legacy.Organizations {
		if organization.Revision < 1 {
			organization.Revision = 1
			legacy.Organizations[id] = organization
		}
	}

	// Versions before the marker used the normalized-table transaction and a
	// separate archive statement. If that older code committed then crashed,
	// application rows already exist but the marker does not. Detect any such
	// durable row, skip the non-idempotent entity import, and still backfill the
	// auth rows that the old importer omitted.
	var priorNormalizedImport bool
	if err = tx.QueryRow(ctx, legacyNormalizedRowsExistSQL).Scan(&priorNormalizedImport); err != nil {
		return err
	}
	if !priorNormalizedImport {
		empty := newState()
		if err = writeIAMDiff(ctx, tx, empty, legacy); err != nil {
			return fmt.Errorf("legacy IAM import failed: %w", err)
		}
		if err = writeWorkersDiff(ctx, tx, empty, legacy); err != nil {
			return fmt.Errorf("legacy Workers import failed: %w", err)
		}
	}
	if err = importLegacyAuthRows(ctx, tx, legacy); err != nil {
		return err
	}
	if err = importLegacyRepositoryRows(ctx, tx, legacy); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO krun_meta (key, value) VALUES ('bootstrapped',$1) ON CONFLICT (key) DO UPDATE SET value=excluded.value`, boolValue(legacy.Bootstrapped)); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO krun_meta (key, value) VALUES ('revision',$1) ON CONFLICT (key) DO UPDATE SET value=GREATEST(krun_meta.value::bigint, excluded.value::bigint)::text`, itoa(legacy.Revision)); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `SELECT setval('krun_revision_seq', GREATEST((SELECT value::bigint FROM krun_meta WHERE key='revision'), 1), (SELECT value::bigint > 0 FROM krun_meta WHERE key='revision'))`); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO krun_meta (key, value) VALUES ('legacy_imported','1') ON CONFLICT (key) DO UPDATE SET value='1'`); err != nil {
		return err
	}
	if err = archiveLegacyTableTx(ctx, tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Every table that can contain durable application state participates in the
// compatibility probe. krun_meta and migration tables are deliberately absent:
// a freshly-created normalized schema contains those rows before import.
const legacyNormalizedRowsExistSQL = `SELECT
	EXISTS (SELECT 1 FROM krun_users) OR
	EXISTS (SELECT 1 FROM krun_organizations) OR
	EXISTS (SELECT 1 FROM krun_members) OR
	EXISTS (SELECT 1 FROM krun_groups) OR
	EXISTS (SELECT 1 FROM krun_assignments) OR
	EXISTS (SELECT 1 FROM krun_resources) OR
	EXISTS (SELECT 1 FROM krun_api_keys) OR
	EXISTS (SELECT 1 FROM krun_invitations) OR
	EXISTS (SELECT 1 FROM krun_repository_connections) OR
	EXISTS (SELECT 1 FROM krun_worker_services) OR
	EXISTS (SELECT 1 FROM krun_worker_builds) OR
	EXISTS (SELECT 1 FROM krun_worker_artifacts) OR
	EXISTS (SELECT 1 FROM krun_worker_releases) OR
	EXISTS (SELECT 1 FROM krun_worker_operations)`

func importLegacyAuthRows(ctx context.Context, tx pgx.Tx, legacy *State) error {
	at := now()
	for hash, session := range legacy.Sessions {
		if !at.Before(session.Absolute) {
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO krun_sessions
			(session_hash, user_id, csrf, auth_version, cli, created_at, seen_at, absolute_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT (session_hash) DO NOTHING`,
			hash, session.User, session.CSRF, session.AuthVersion, session.CLI, session.Created, session.Seen, session.Absolute); err != nil {
			return fmt.Errorf("legacy session import failed: %w", err)
		}
	}
	for hash, browser := range legacy.Browsers {
		if !at.Before(browser.Expires) {
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO krun_browsers (browser_hash, csrf, generation, expires_at)
			VALUES ($1,$2,$3,$4) ON CONFLICT (browser_hash) DO NOTHING`, hash, browser.CSRF, browser.Generation, browser.Expires); err != nil {
			return fmt.Errorf("legacy browser import failed: %w", err)
		}
	}
	for hash, login := range legacy.Logins {
		if login.Consumed || !at.Before(login.Expires) {
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO krun_login_states
			(state_hash, provider, browser_hash, fingerprint, verifier, nonce, return_to, generation, expires_at, consumed)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT (state_hash) DO NOTHING`, hash,
			login.Provider, login.Browser, login.Fingerprint, login.Verifier, login.Nonce, login.ReturnTo,
			login.Generation, login.Expires, login.Consumed); err != nil {
			return fmt.Errorf("legacy login state import failed: %w", err)
		}
	}
	for hash, login := range legacy.EmailLogins {
		if login.Consumed || login.Attempts >= 5 || !at.Before(login.Expires) {
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO krun_email_logins
			(challenge_hash, provider, browser_hash, fingerprint, email, code_hash, return_to, ip, generation, attempts, sent_at, expires_at, consumed)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13) ON CONFLICT (challenge_hash) DO NOTHING`, hash,
			login.Provider, login.Browser, login.Fingerprint, login.Email, login.CodeHash, login.ReturnTo,
			login.IP, login.Generation, login.Attempts, login.Sent, login.Expires, login.Consumed); err != nil {
			return fmt.Errorf("legacy email login import failed: %w", err)
		}
	}
	type legacyDevice struct {
		hash   string
		device Device
	}
	devices := make([]legacyDevice, 0, len(legacy.Devices))
	for hash, device := range legacy.Devices {
		devices = append(devices, legacyDevice{hash: hash, device: device})
	}
	// v6 made user_code unique. An older snapshot may predate that invariant;
	// keep the code with the longest remaining validity in that case.
	sort.Slice(devices, func(i, j int) bool {
		if devices[i].device.Expires.Equal(devices[j].device.Expires) {
			return devices[i].hash < devices[j].hash
		}
		return devices[i].device.Expires.After(devices[j].device.Expires)
	})
	for _, entry := range devices {
		hash, device := entry.hash, entry.device
		if device.Consumed || !at.Before(device.Expires) {
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO krun_device_codes
			(code_hash, user_code, token, expires_at, approved, consumed)
			VALUES ($1,$2,$3,$4,$5,$6)
			ON CONFLICT DO NOTHING`, hash, device.UserCode, device.Token, device.Expires, device.Approved, device.Consumed); err != nil {
			return fmt.Errorf("legacy device code import failed: %w", err)
		}
	}
	return nil
}

func importLegacyRepositoryRows(ctx context.Context, tx pgx.Tx, legacy *State) error {
	for _, connection := range legacy.RepositoryConnections {
		if _, err := tx.Exec(ctx, `INSERT INTO krun_repository_connections
			(user_id, provider_id, access_token, reconnect_required, connected_at)
			VALUES ($1,$2,$3,true,$4)
			ON CONFLICT (user_id, provider_id) DO NOTHING`, connection.User, connection.Provider,
			connection.AccessToken, connection.ConnectedAt); err != nil {
			return fmt.Errorf("legacy repository connection import failed: %w", err)
		}
	}
	at := now()
	for state, pending := range legacy.RepositoryConnectStates {
		if !at.Before(pending.Expires) {
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO krun_repository_connect_states
			(state, user_id, provider_id, org_id, browser_hash, expires_at)
			VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT (state) DO NOTHING`, state, pending.User,
			pending.Provider, pending.Organization, pending.Browser, pending.Expires); err != nil {
			return fmt.Errorf("legacy repository connect state import failed: %w", err)
		}
	}
	return nil
}

func boolValue(v bool) string {
	if v {
		return "1"
	}
	return "0"
}

// archiveLegacyTableTx retires the document table in the caller's import
// transaction. Keeping an existing archive is safer than overwriting the only
// rollback copy left by an older release.
func archiveLegacyTableTx(ctx context.Context, tx pgx.Tx) error {
	var archivedExists bool
	if err := tx.QueryRow(ctx, `SELECT to_regclass(current_schema() || '.krun_control_state_legacy') IS NOT NULL`).Scan(&archivedExists); err != nil {
		return err
	}
	statement := `ALTER TABLE krun_control_state RENAME TO krun_control_state_legacy`
	if archivedExists {
		statement = `DROP TABLE krun_control_state`
	}
	_, err := tx.Exec(ctx, statement)
	return err
}
