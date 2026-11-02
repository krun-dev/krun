package control

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

var testPools sync.Map

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
