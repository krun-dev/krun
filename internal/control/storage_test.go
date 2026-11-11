package control

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	appconfig "github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/testpgx"
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
	schema := testpgx.NewSchema(t, "krun_control")
	ctx := context.Background()
	return func() *Store {
		t.Helper()
		store, err := openStoreForTest(t, ctx, schema.DSN)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(store.Close)
		return store
	}
}

func openStoreForTest(t *testing.T, ctx context.Context, dsn string) (*Store, error) {
	t.Helper()
	key := sha256.Sum256([]byte("repository connection integration test"))
	cfg := appconfig.Config{
		DB: dsn,
		Workers: &appconfig.WorkersConfig{
			SecretsActiveKey: "test",
			SecretsKeys:      map[string]string{"test": base64.StdEncoding.EncodeToString(key[:])},
		},
	}
	store, err := OpenStore(ctx, cfg)
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
