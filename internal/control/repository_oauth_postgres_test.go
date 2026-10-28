package control

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestPostgresRepositoryRefreshClaimHasSingleWinner(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	connection := WorkerRepositoryConnection{
		User: "refresh-user", Provider: "gitlab", AccessToken: "old-access",
		TokenType: "bearer", RefreshToken: "old-refresh",
		ExpiresAt: now().Add(-time.Minute), ConnectedAt: now(),
	}
	if err := store.upsertRepositoryConnectionAudited(ctx, connection, nil); err != nil {
		t.Fatal(err)
	}
	current, ok, err := store.repositoryConnection(ctx, connection.User, connection.Provider)
	if err != nil || !ok || current.TokenRevision != 1 {
		t.Fatalf("initial revision=%d ok=%v err=%v", current.TokenRevision, ok, err)
	}

	const contenders = 8
	type result struct {
		connection WorkerRepositoryConnection
		claimed    bool
		err        error
	}
	results := make(chan result, contenders)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range contenders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			claimed, won, claimErr := store.claimRepositoryConnectionRefresh(ctx, current.User, current.Provider, current.TokenRevision, now())
			results <- result{claimed, won, claimErr}
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	winners := []WorkerRepositoryConnection{}
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.claimed {
			winners = append(winners, result.connection)
		}
	}
	if len(winners) != 1 || winners[0].TokenRevision != 2 || winners[0].RefreshState != "refreshing" {
		t.Fatalf("refresh claim winners=%d", len(winners))
	}

	rotated := winners[0]
	rotated.AccessToken = "new-access"
	rotated.RefreshToken = "new-refresh"
	rotated.ExpiresAt = now().Add(2 * time.Hour)
	completed, err := store.completeRepositoryConnectionRefresh(ctx, rotated, winners[0].TokenRevision)
	if err != nil || !completed {
		t.Fatalf("complete claimed refresh: completed=%v err=%v", completed, err)
	}
	if stale, staleErr := store.completeRepositoryConnectionRefresh(ctx, rotated, winners[0].TokenRevision); staleErr != nil || stale {
		t.Fatalf("stale refresh completion accepted: completed=%v err=%v", stale, staleErr)
	}
	current, ok, err = store.repositoryConnection(ctx, connection.User, connection.Provider)
	if err != nil || !ok || current.AccessToken != "new-access" || current.RefreshToken != "new-refresh" || current.TokenRevision != 3 || current.RefreshState != "ready" {
		t.Fatalf("rotated grant was not committed atomically: revision=%d state=%q ok=%v err=%v", current.TokenRevision, current.RefreshState, ok, err)
	}
}
