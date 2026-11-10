package control

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"sync"
	"testing"
	"time"

	appconfig "github.com/krun-dev/krun/internal/config"
)

// KRUN_TEST_POSTGRES_URL must point at an isolated disposable test database.
func TestPostgresSharedTransactions(t *testing.T) {
	open := postgresTestOpener(t)
	a := open()
	b := open()
	ctx := context.Background()
	if e := a.Update(ctx, PartitionAll, func(st *State) error {
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
	if e := a.Update(ctx, PartitionAll, func(st *State) error {
		st.Users["pg-user"] = User{ID: "pg-user", Status: "disabled"}
		return errors.New("rollback")
	}); e == nil {
		t.Fatal("expected transaction rollback")
	}
	if e := b.Update(ctx, PartitionAll, func(st *State) error {
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

func TestPostgresBrowserLoginSurvivesRestart(t *testing.T) {
	open := postgresTestOpener(t)
	store := open()
	ctx := context.Background()
	if err := store.Update(ctx, PartitionAll, func(st *State) error { *st = *newState(); return nil }); err != nil {
		t.Fatal(err)
	}
	c := appconfig.Config{Development: true, PublicURL: "http://127.0.0.1:8080", DB: "postgres://test-only/krun_test", Providers: []appconfig.ProviderConfig{{ID: "local", Source: "pg-local", Type: "dev", Registration: "jit"}}}
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
	store = open()
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
	store = open()
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
	_, _ = postgresTestPool(store).Exec(ctx, "DELETE FROM krun_audits")
	// One expired, one fresh entry.
	if err := store.Update(ctx, PartitionAll, func(st *State) error {
		st.RecordAudit(Audit{ID: "old", Time: now().Add(-8 * 24 * time.Hour)})
		st.RecordAudit(Audit{ID: "new", Time: now()})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := postgresTestPool(store).QueryRow(ctx, "SELECT count(*) FROM krun_audits").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatal("audits were not persisted to the audit table", count)
	}
	if err := store.DeleteAuditsBefore(ctx, now().Add(-auditRetention)); err != nil {
		t.Fatal(err)
	}
	if err := postgresTestPool(store).QueryRow(ctx, "SELECT count(*) FROM krun_audits WHERE id='new'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("retention did not keep only the fresh entry", count)
	}
	store.Close()
}
