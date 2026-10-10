package control

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
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
	if e = a.Update(ctx, func(st *State) error {
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
	if e = a.Update(ctx, func(st *State) error {
		st.Users["pg-user"] = User{ID: "pg-user", Status: "disabled"}
		return errors.New("rollback")
	}); e == nil {
		t.Fatal("expected transaction rollback")
	}
	if e = b.Update(ctx, func(st *State) error {
		if st.Users["pg-user"].Status != "active" {
			t.Error("rolled back write visible across replica")
		}
		if len(st.Assignments) != 1 || len(st.Audits) != 1 {
			t.Error("bootstrap and audit not atomic")
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
}

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
	t.Cleanup(store.Close)
	return store
}

func TestPostgresBrowserLoginSurvivesRestart(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	if err := store.Update(ctx, func(st *State) error { *st = *newState(); return nil }); err != nil {
		t.Fatal(err)
	}
	c := Config{Development: true, PublicURL: "http://127.0.0.1:8080", DB: "postgres://test-only/krun_test", Providers: []ProviderConfig{{ID: "local", Source: "pg-local", Type: "dev", Registration: "jit"}}}
	s, err := NewServer(ctx, c, store)
	if err != nil {
		t.Fatal(err)
	}
	csrfResponse := browserRequest(s, "GET", "/auth/csrf", nil, nil)
	var csrf struct {
		Token string `json:"csrf_token"`
	}
	if err := json.Unmarshal(csrfResponse.Body.Bytes(), &csrf); err != nil {
		t.Fatal(err)
	}
	cookies := csrfResponse.Result().Cookies()
	begin := browserRequest(s, "POST", "/auth/local/login", url.Values{"csrf_token": {csrf.Token}}, cookies)
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
	if complete.Header().Get("Location") != "/console/" {
		t.Fatal("pending login lost across restart", complete.Header().Get("Location"))
	}
	cookies = append(cookies, complete.Result().Cookies()...)
	store.Close()
	store = postgresTestStore(t)
	s, err = NewServer(ctx, c, store)
	if err != nil {
		t.Fatal(err)
	}
	if me := browserRequest(s, "GET", "/auth/me", nil, cookies); me.Code != 200 {
		t.Fatal("session lost across restart", me.Code)
	}
	csrfResponse = browserRequest(s, "GET", "/auth/csrf", nil, cookies)
	if csrfResponse.Code != 200 || json.Unmarshal(csrfResponse.Body.Bytes(), &csrf) != nil {
		t.Fatal("persisted CSRF cannot be read")
	}
	if logout := browserRequest(s, "POST", "/auth/logout", url.Values{"csrf_token": {csrf.Token}}, cookies); logout.Code != 204 {
		t.Fatal("logout failed after restart", logout.Code)
	}
	if me := browserRequest(s, "GET", "/auth/me", nil, cookies); me.Code != 401 {
		t.Fatal("logout did not revoke persisted session")
	}
}

func TestPostgresAuditRetentionPersists(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	state := newState()
	state.Audits = []Audit{{ID: "old", Time: now().Add(-8 * 24 * time.Hour)}, {ID: "new", Time: now()}}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	// Seed through SQL to exercise cleanup of previously stored expired rows.
	if _, err = store.pool.Exec(ctx, "UPDATE krun_control_state SET data=$1 WHERE id=1", raw); err != nil {
		t.Fatal(err)
	}
	if err = store.Update(ctx, func(st *State) error {
		if len(st.Audits) != 1 || st.Audits[0].ID != "new" {
			t.Error("expired audit was loaded")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	store.Close()
	reopened := postgresTestStore(t)
	if err = reopened.pool.QueryRow(ctx, "SELECT data FROM krun_control_state WHERE id=1").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var persisted State
	if err = json.Unmarshal(raw, &persisted); err != nil {
		t.Fatal(err)
	}
	if len(persisted.Audits) != 1 || persisted.Audits[0].ID != "new" {
		t.Fatal("expired audit persisted after restart")
	}
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
	if err = store.Update(ctx, func(st *State) error { calls++; st.Revision++; return nil }); err != nil {
		t.Fatal("failed to replace terminated connection", err)
	}
	if calls != 1 {
		t.Fatal("state mutation retried")
	}
}
