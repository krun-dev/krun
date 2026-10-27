package control

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"
)

func TestPostgresLegacyImportPreservesActiveAuthRowsWithoutUsers(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	cleanupLegacyMigrationTest(t, store)
	defer cleanupLegacyMigrationTest(t, store)

	at := now()
	legacy := newState()
	legacy.Revision = 17
	legacy.Sessions["session-hash"] = Session{
		User: "legacy-user", Hash: "session-hash", CSRF: "session-csrf", AuthVersion: 3,
		Created: at.Add(-time.Hour), Seen: at, Absolute: at.Add(time.Hour), CLI: true,
	}
	legacy.Browsers["browser-hash"] = Browser{CSRF: "browser-csrf", Generation: 4, Expires: at.Add(time.Hour)}
	legacy.Logins["login-state"] = Login{
		Provider: "oidc", Browser: "browser-hash", Fingerprint: "fingerprint", Verifier: "verifier",
		Nonce: "nonce", ReturnTo: "/", Generation: 4, Expires: at.Add(time.Hour),
	}
	legacy.EmailLogins["email-challenge"] = EmailLogin{
		Provider: "email", Browser: "browser-hash", Fingerprint: "fingerprint", Email: "legacy@example.invalid",
		CodeHash: "code-hash", ReturnTo: "/", IP: "192.0.2.1", Generation: 4, Attempts: 1,
		Sent: at, Expires: at.Add(time.Hour),
	}
	legacy.Devices["device-hash"] = Device{
		CodeHash: "device-hash", UserCode: "LEGACY01", Token: "device-token", Expires: at.Add(time.Hour), Approved: true,
	}
	legacy.RepositoryConnections["legacy-connection"] = WorkerRepositoryConnection{
		User: "legacy-user", Provider: "gitlab", AccessToken: "repository-token", ConnectedAt: at,
	}
	legacy.RepositoryConnectStates["repository-state"] = WorkerRepositoryConnectState{
		User: "legacy-user", Provider: "gitlab", Organization: "legacy-org", Browser: "browser-hash", Expires: at.Add(time.Hour),
	}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.pool.Exec(ctx, `CREATE TABLE krun_control_state (id integer PRIMARY KEY, data jsonb NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err = store.pool.Exec(ctx, `INSERT INTO krun_control_state (id, data) VALUES (1,$1)`, raw); err != nil {
		t.Fatal(err)
	}
	var starts sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		starts.Add(1)
		go func() {
			defer starts.Done()
			errs <- store.importLegacySnapshot(ctx)
		}()
	}
	starts.Wait()
	close(errs)
	for importErr := range errs {
		if importErr != nil {
			t.Fatal(importErr)
		}
	}

	for table := range map[string]struct{}{
		"krun_sessions": {}, "krun_browsers": {}, "krun_login_states": {}, "krun_email_logins": {},
		"krun_device_codes": {}, "krun_repository_connections": {}, "krun_repository_connect_states": {},
	} {
		var count int
		if err = store.pool.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("%s count=%d, want 1", table, count)
		}
	}
	var repositoryReconnect bool
	if err = store.pool.QueryRow(ctx, `SELECT reconnect_required FROM krun_repository_connections
		WHERE user_id='legacy-user' AND provider_id='gitlab'`).Scan(&repositoryReconnect); err != nil {
		t.Fatal(err)
	}
	if !repositoryReconnect {
		t.Fatal("legacy OAuth connection imported as a live permanent grant")
	}
	var marker, revision string
	if err = store.pool.QueryRow(ctx, `SELECT value FROM krun_meta WHERE key='legacy_imported'`).Scan(&marker); err != nil {
		t.Fatal(err)
	}
	if err = store.pool.QueryRow(ctx, `SELECT value FROM krun_meta WHERE key='revision'`).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if marker != "1" || revision != "17" {
		t.Fatalf("marker=%q revision=%q", marker, revision)
	}
	var live, archived bool
	if err = store.pool.QueryRow(ctx, `SELECT to_regclass(current_schema() || '.krun_control_state') IS NOT NULL,
		to_regclass(current_schema() || '.krun_control_state_legacy') IS NOT NULL`).Scan(&live, &archived); err != nil {
		t.Fatal(err)
	}
	if live || !archived {
		t.Fatalf("legacy tables live=%v archived=%v", live, archived)
	}
	// With no live source table, replay is a no-op rather than a duplicate
	// import; this also covers the valid zero-user legacy state.
	if err = store.importLegacySnapshot(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresLegacyImportBackfillsAuthAfterOldImporterCrash(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	cleanupLegacyMigrationTest(t, store)
	defer cleanupLegacyMigrationTest(t, store)

	// A durable application row with no marker models the previous importer
	// committing its normalized transaction and crashing before ALTER TABLE.
	if _, err := store.pool.Exec(ctx, `INSERT INTO krun_worker_services (service_id, org_id) VALUES ('existing-service','legacy-org')`); err != nil {
		t.Fatal(err)
	}
	legacy := newState()
	legacy.Sessions["recovered-session"] = Session{
		User: "legacy-user", CSRF: "csrf", AuthVersion: 1, Created: now(), Seen: now(), Absolute: now().Add(time.Hour),
	}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.pool.Exec(ctx, `CREATE TABLE krun_control_state (id integer PRIMARY KEY, data jsonb NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err = store.pool.Exec(ctx, `INSERT INTO krun_control_state (id, data) VALUES (1,$1)`, raw); err != nil {
		t.Fatal(err)
	}
	if err = store.importLegacySnapshot(ctx); err != nil {
		t.Fatal(err)
	}
	var sessions int
	if err = store.pool.QueryRow(ctx, `SELECT count(*) FROM krun_sessions WHERE session_hash='recovered-session'`).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if sessions != 1 {
		t.Fatalf("recovered sessions=%d, want 1", sessions)
	}
}

func TestPostgresLegacyImportFailsClosedWhenRollbackTableReappears(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	cleanupLegacyMigrationTest(t, store)
	defer cleanupLegacyMigrationTest(t, store)

	if _, err := store.pool.Exec(ctx, `CREATE TABLE krun_control_state (id integer PRIMARY KEY, data jsonb NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO krun_control_state (id, data) VALUES (1,'{}'::jsonb)`); err != nil {
		t.Fatal(err)
	}
	if err := store.importLegacySnapshot(ctx); err != nil {
		t.Fatal(err)
	}
	// A rolled-back binary writes a new fixed-name table while the archived
	// pre-import copy and marker still exist. A later upgrade must not silently
	// drop those new writes.
	if _, err := store.pool.Exec(ctx, `CREATE TABLE krun_control_state (id integer PRIMARY KEY, data jsonb NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO krun_control_state (id, data) VALUES (1,'{"revision":99}'::jsonb)`); err != nil {
		t.Fatal(err)
	}
	if err := store.importLegacySnapshot(ctx); err == nil {
		t.Fatal("rollback writes were silently discarded")
	}
	var live, archived bool
	if err := store.pool.QueryRow(ctx, `SELECT to_regclass(current_schema() || '.krun_control_state') IS NOT NULL,
		to_regclass(current_schema() || '.krun_control_state_legacy') IS NOT NULL`).Scan(&live, &archived); err != nil {
		t.Fatal(err)
	}
	if !live || !archived {
		t.Fatalf("fail-closed migration changed source tables: live=%v archived=%v", live, archived)
	}
}

func TestPostgresMigrationFromVersionFiveAddsArtifactUsageRevision(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	if _, err := store.pool.Exec(ctx, `ALTER TABLE krun_worker_artifacts DROP COLUMN IF EXISTS use_revision`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `DELETE FROM krun_schema_migrations WHERE version > 5`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `UPDATE krun_meta SET value='5' WHERE key='schema_version'`); err != nil {
		t.Fatal(err)
	}
	if err := applyStoreMigrations(ctx, store.pool); err != nil {
		t.Fatal(err)
	}
	var exists bool
	if err := store.pool.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM information_schema.columns
		WHERE table_schema=current_schema() AND table_name='krun_worker_artifacts' AND column_name='use_revision'
	)`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("version-five database was not upgraded with use_revision")
	}
}

func TestPostgresMigrationFromVersionNineAddsWorkerServiceRevisions(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	if _, err := store.pool.Exec(ctx, `
ALTER TABLE krun_worker_services DROP COLUMN IF EXISTS domains_revision;
ALTER TABLE krun_worker_services DROP COLUMN IF EXISTS preview_revision;
DELETE FROM krun_schema_migrations WHERE version > 9;
UPDATE krun_meta SET value='9' WHERE key='schema_version';
`); err != nil {
		t.Fatal(err)
	}
	if err := applyStoreMigrations(ctx, store.pool); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns
		WHERE table_schema=current_schema() AND table_name='krun_worker_services'
		  AND column_name IN ('domains_revision','preview_revision')`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("worker service revision columns=%d, want 2", count)
	}
}

func cleanupLegacyMigrationTest(t *testing.T, store *Store) {
	t.Helper()
	ctx := context.Background()
	for _, statement := range []string{
		`DROP TABLE IF EXISTS krun_control_state`,
		`DROP TABLE IF EXISTS krun_control_state_legacy`,
		`DELETE FROM krun_meta WHERE key='legacy_imported'`,
	} {
		if _, err := store.pool.Exec(ctx, statement); err != nil {
			t.Errorf("legacy migration cleanup: %v", err)
		}
	}
}
