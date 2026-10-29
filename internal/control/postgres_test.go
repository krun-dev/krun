package control

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// KRUN_TEST_POSTGRES_URL must point at an isolated disposable test database.
func TestPostgresSharedTransactions(t *testing.T) {
	dsn := os.Getenv("KRUN_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set KRUN_TEST_POSTGRES_URL for PostgreSQL integration")
	}
	ctx := context.Background()
	a, e := OpenStore(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	b, e := OpenStore(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	if e = a.Update(ctx, PartitionAll, func(st *State) error {
		*st = *newState()
		st.Users["pg-user"] = User{ID: "pg-user", Status: "active"}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, s := range []*Store{a, b} {
		wg.Add(1)
		go func(store *Store) { defer wg.Done(); errs <- Bootstrap(ctx, store, "pg-user") }(s)
	}
	wg.Wait()
	close(errs)
	success := 0
	for e := range errs {
		if e == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("expected one bootstrap commit, got %d", success)
	}
	if e = a.Update(ctx, PartitionAll, func(st *State) error {
		st.Users["pg-user"] = User{ID: "pg-user", Status: "disabled"}
		return errors.New("rollback")
	}); e == nil {
		t.Fatal("expected transaction rollback")
	}
	if e = b.Update(ctx, PartitionAll, func(st *State) error {
		if st.Users["pg-user"].Status != "active" {
			t.Error("rolled back write visible across replica")
		}
		if len(st.Assignments) != 1 {
			t.Error("bootstrap and audit not atomic")
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
}

// truncatedTests records which tests already started from a clean database, so
// a test that deliberately reopens the store mid-run keeps its rows.
var truncatedTests sync.Map

func postgresTestStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("KRUN_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set KRUN_TEST_POSTGRES_URL for PostgreSQL integration")
	}
	store, err := OpenStore(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	// Cleanups run last-registered-first: register Close first so the truncate
	// runs while the pool is still live. Tests that close it themselves are
	// handled by truncateControlTables's closed-pool guard.
	t.Cleanup(store.Close)
	t.Cleanup(func() { truncateControlTables(t, store) })
	if _, seen := truncatedTests.LoadOrStore(t.Name(), true); !seen {
		truncateControlTables(t, store)
	}
	return store
}

func TestPostgresUsesSingleSchemaBaseline(t *testing.T) {
	dsn := os.Getenv("KRUN_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set KRUN_TEST_POSTGRES_URL for PostgreSQL integration")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := "krun_baseline_" + strconv.FormatInt(time.Now().UnixNano(), 10)
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, `CREATE SCHEMA `+identifier); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = admin.Exec(context.Background(), `DROP SCHEMA `+identifier+` CASCADE`) }()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := u.Query()
	query.Set("search_path", schema)
	u.RawQuery = query.Encode()
	store, err := OpenStore(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var migrationLedger, legacyState, legacyArchive bool
	if err := store.pool.QueryRow(ctx, `SELECT
		to_regclass(current_schema() || '.krun_schema_migrations') IS NOT NULL,
		to_regclass(current_schema() || '.krun_control_state') IS NOT NULL,
		to_regclass(current_schema() || '.krun_control_state_legacy') IS NOT NULL`).Scan(
		&migrationLedger, &legacyState, &legacyArchive); err != nil {
		t.Fatal(err)
	}
	if migrationLedger || legacyState || legacyArchive {
		t.Fatalf("compatibility tables exist: migrations=%t state=%t archive=%t", migrationLedger, legacyState, legacyArchive)
	}
	var poolColumns, placementTables int
	if err := store.pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM information_schema.columns
		 WHERE table_schema=current_schema()
		   AND table_name IN ('krun_worker_services','krun_worker_releases')
		   AND column_name='pool'),
		(SELECT count(*) FROM information_schema.tables
		 WHERE table_schema=current_schema() AND table_name='krun_worker_service_placements')`).Scan(
		&poolColumns, &placementTables); err != nil {
		t.Fatal(err)
	}
	if poolColumns != 0 || placementTables != 1 {
		t.Fatalf("baseline model: pool columns=%d placement tables=%d", poolColumns, placementTables)
	}
}

// truncateControlTables empties every control-plane table. Sessions, browsers
// and login transactions are included because rate limits and expiry are
// checked against them. A closed pool is not an error: tests may restart the
// store themselves.
func truncateControlTables(t *testing.T, store *Store) {
	t.Helper()
	if store.pool == nil || store.pool.Ping(context.Background()) != nil {
		return
	}
	tables := []string{
		"worker_config_ops", "worker_config_revisions", "worker_config_routes", "worker_config_releases", "worker_runtime_acks",
		"krun_rate_limits", "krun_audits",
		"krun_sessions", "krun_browsers", "krun_login_states", "krun_email_logins",
		"krun_device_codes", "krun_invitations", "krun_repository_connect_states",
		"krun_repository_connections", "krun_api_key_hashes", "krun_api_keys",
		"krun_assignments", "krun_group_members", "krun_groups", "krun_members",
		"krun_org_sources", "krun_organizations", "krun_resources", "krun_users",
		"krun_identity_providers", "krun_identity_sources",
		"krun_worker_history", "krun_worker_idempotency", "krun_worker_operations",
		"krun_worker_service_placements",
		"krun_worker_release_secrets", "krun_worker_releases", "krun_worker_environment_secrets",
		"krun_worker_secret_versions", "krun_worker_artifacts", "krun_worker_build_private",
		"krun_worker_source_uploads",
		"krun_worker_builds", "krun_worker_environments", "krun_worker_service_domains",
		"krun_worker_services",
	}
	for _, table := range tables {
		if _, err := store.pool.Exec(context.Background(), `DELETE FROM `+table); err != nil {
			return
		}
	}
	_, _ = store.pool.Exec(context.Background(), `DELETE FROM krun_meta WHERE key IN ('revision','bootstrapped','worker_config_head','worker_topology_generation')`)
	_, _ = store.pool.Exec(context.Background(), `INSERT INTO krun_meta (key,value) VALUES
		('worker_config_head','0'),('worker_topology_generation','0') ON CONFLICT (key) DO NOTHING`)
}

func TestPostgresBrowserLoginSurvivesRestart(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	if err := store.Update(ctx, PartitionAll, func(st *State) error { *st = *newState(); return nil }); err != nil {
		t.Fatal(err)
	}
	c := Config{Development: true, PublicURL: "http://127.0.0.1:8080", DB: "postgres://test-only/krun_test", Providers: []ProviderConfig{{ID: "local", Source: "pg-local", Type: "dev", Registration: "jit"}}}
	s, err := NewServer(ctx, c, store)
	if err != nil {
		t.Fatal(err)
	}
	csrfResponse := browserRequest(s, "POST", "/api/auth/csrf", nil, nil)
	var csrf struct {
		Token string `json:"csrf_token"`
	}
	if err := json.Unmarshal(csrfResponse.Body.Bytes(), &csrf); err != nil {
		t.Fatal(err)
	}
	cookies := csrfResponse.Result().Cookies()
	begin := browserRequest(s, "POST", "/api/auth/local/login", url.Values{"csrf_token": {csrf.Token}}, cookies)
	if begin.Code != 303 {
		t.Fatal("login begin failed", begin.Code)
	}
	store.Close()
	store = postgresTestStore(t)
	s, err = NewServer(ctx, c, store)
	if err != nil {
		t.Fatal(err)
	}
	complete := browserRequest(s, "GET", begin.Header().Get("Location"), nil, cookies)
	if complete.Header().Get("Location") != "/" {
		t.Fatal("pending login lost across restart", complete.Header().Get("Location"))
	}
	cookies = append(cookies, complete.Result().Cookies()...)
	store.Close()
	store = postgresTestStore(t)
	s, err = NewServer(ctx, c, store)
	if err != nil {
		t.Fatal(err)
	}
	if me := browserRequest(s, "GET", "/api/auth/me", nil, cookies); me.Code != 200 {
		t.Fatal("session lost across restart", me.Code)
	}
	csrfResponse = browserRequest(s, "POST", "/api/auth/csrf", nil, cookies)
	if csrfResponse.Code != 200 || json.Unmarshal(csrfResponse.Body.Bytes(), &csrf) != nil {
		t.Fatal("persisted CSRF cannot be read")
	}
	if logout := browserRequest(s, "POST", "/api/auth/logout", url.Values{"csrf_token": {csrf.Token}}, cookies); logout.Code != 204 {
		t.Fatal("logout failed after restart", logout.Code)
	}
	if me := browserRequest(s, "GET", "/api/auth/me", nil, cookies); me.Code != 401 {
		t.Fatal("logout did not revoke persisted session")
	}
}

func TestPostgresAuditRetentionPersists(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	_, _ = store.pool.Exec(ctx, "DELETE FROM krun_audits")
	// One expired, one fresh entry.
	if err := store.Update(ctx, PartitionAll, func(st *State) error {
		st.recordAudit(Audit{ID: "old", Time: now().Add(-8 * 24 * time.Hour)})
		st.recordAudit(Audit{ID: "new", Time: now()})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.pool.QueryRow(ctx, "SELECT count(*) FROM krun_audits").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatal("audits were not persisted to the audit table", count)
	}
	if err := store.deleteAuditsBefore(ctx, now().Add(-auditRetention)); err != nil {
		t.Fatal(err)
	}
	if err := store.pool.QueryRow(ctx, "SELECT count(*) FROM krun_audits WHERE id='new'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("retention did not keep only the fresh entry", count)
	}
	store.Close()
}

func TestOpenStoreRequiresPostgres(t *testing.T) {
	if _, err := OpenStore(context.Background(), ""); err == nil {
		t.Fatal("store without PostgreSQL accepted")
	}
}

func TestPostgresReconnectsBeforeMutation(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	conn, err := store.pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	pid := conn.Conn().PgConn().PID()
	conn.Release()
	killer, err := pgx.Connect(ctx, os.Getenv("KRUN_TEST_POSTGRES_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer killer.Close(ctx)
	var terminated bool
	if err = killer.QueryRow(ctx, "SELECT pg_terminate_backend($1)", pid).Scan(&terminated); err != nil || !terminated {
		t.Fatal("terminate pooled connection", err)
	}
	calls := 0
	if err = store.Update(ctx, PartitionAll, func(st *State) error { calls++; st.Revision++; return nil }); err != nil {
		t.Fatal("failed to replace terminated connection", err)
	}
	if calls != 1 {
		t.Fatal("state mutation retried")
	}
}
