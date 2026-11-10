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
)

var testPools sync.Map

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

func memorySnapshot(t *testing.T, store *Store) *State {
	t.Helper()
	var state *State
	if err := store.View(context.Background(), PartitionAll, func(st *State) error { state = st; return nil }); err != nil {
		t.Fatal(err)
	}
	return state
}

func memoryAudits(t *testing.T, store *Store) []Audit {
	t.Helper()
	var audits []Audit
	if err := store.View(context.Background(), PartitionIAM, func(*State) error {
		var err error
		audits, err = store.ReadAudits(context.Background(), time.Time{}, "", 10000)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return audits
}
