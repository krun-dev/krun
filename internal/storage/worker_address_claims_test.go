package storage

import (
	"context"
	"sync"
	"testing"
)

func TestWorkerAddressClaimsAreAtomic(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, owner := range []string{"org-a", "org-b"} {
		wg.Add(1)
		go func(owner string) {
			defer wg.Done()
			tx, err := store.pool.Begin(ctx)
			if err != nil {
				results <- err
				return
			}
			defer tx.Rollback(ctx)
			if err := claimWorkerAddress(ctx, tx, "orders", "worker", owner); err != nil {
				results <- err
				return
			}
			results <- tx.Commit(ctx)
		}(owner)
	}
	wg.Wait()
	close(results)
	succeeded := 0
	for err := range results {
		if err == nil {
			succeeded++
		}
	}
	if succeeded != 1 {
		t.Fatalf("expected one successful reservation, got %d", succeeded)
	}
}
