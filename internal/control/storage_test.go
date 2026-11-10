package control

import (
	"context"
	"net/url"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var testPools sync.Map

func postgresTestStore(t *testing.T) *Store {
	t.Helper()
	return postgresTestOpener(t)()
}

// Each opener owns one schema. Tests can reopen it to exercise restarts or
// multiple replicas without sharing tables with any other test.
func postgresTestOpener(t *testing.T) func() *Store {
	t.Helper()
	dsn := os.Getenv("KRUN_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set KRUN_TEST_POSTGRES_URL for PostgreSQL integration")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)

	name := "krun_control_" + strconv.FormatInt(time.Now().UnixNano(), 10)
	schema := pgx.Identifier{name}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Error(err)
		}
	})

	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", name)
	parsed.RawQuery = query.Encode()
	return func() *Store {
		t.Helper()
		store, err := openStoreForTest(t, ctx, parsed.String())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(store.Close)
		return store
	}
}

func openStoreForTest(t *testing.T, ctx context.Context, dsn string) (*Store, error) {
	t.Helper()
	store, err := OpenStore(ctx, dsn)
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		store.Close()
		return nil, err
	}
	testPools.Store(store, pool)
	t.Cleanup(func() { testPools.Delete(store); pool.Close() })
	return store, nil
}

func postgresTestPool(store *Store) *pgxpool.Pool {
	value, ok := testPools.Load(store)
	if !ok {
		return nil
	}
	return value.(*pgxpool.Pool)
}

func testAudits(t *testing.T, store *Store) []Audit {
	t.Helper()
	audits, err := store.ReadAudits(t.Context(), time.Time{}, "", 10000)
	if err != nil {
		t.Fatal(err)
	}
	return audits
}

func expireTestSession(t *testing.T, store *Store, token string) {
	t.Helper()
	tag, err := postgresTestPool(store).Exec(t.Context(), "UPDATE krun_sessions SET absolute_at=now()-interval '1 minute' WHERE session_hash=$1", digest(token))
	if err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("expire session: %v, %v", tag, err)
	}
}
