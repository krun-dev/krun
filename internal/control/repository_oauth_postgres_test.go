package control

import (
	"bytes"
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
	if err := store.UpsertRepositoryConnectionAudited(ctx, connection, nil); err != nil {
		t.Fatal(err)
	}
	current, ok, err := store.RepositoryConnection(ctx, connection.User, connection.Provider)
	if err != nil || !ok || current.TokenRevision <= 0 {
		t.Fatalf("initial revision=%d ok=%v err=%v", current.TokenRevision, ok, err)
	}

	assertRepositoryTokenCiphertext(t, store, connection)

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
			claimed, won, claimErr := store.ClaimRepositoryConnectionRefresh(ctx, current.User, current.Provider, current.TokenRevision, now())
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
	if len(winners) != 1 || winners[0].TokenRevision <= current.TokenRevision || winners[0].RefreshState != "refreshing" {
		t.Fatalf("refresh claim winners=%d", len(winners))
	}

	rotated := winners[0]
	rotated.AccessToken = "new-access"
	rotated.RefreshToken = "new-refresh"
	rotated.ExpiresAt = now().Add(2 * time.Hour)
	completed, err := store.CompleteRepositoryConnectionRefresh(ctx, rotated, winners[0].TokenRevision)
	if err != nil || !completed {
		t.Fatalf("complete claimed refresh: completed=%v err=%v", completed, err)
	}
	if stale, staleErr := store.CompleteRepositoryConnectionRefresh(ctx, rotated, winners[0].TokenRevision); staleErr != nil || stale {
		t.Fatalf("stale refresh completion accepted: completed=%v err=%v", stale, staleErr)
	}
	current, ok, err = store.RepositoryConnection(ctx, connection.User, connection.Provider)
	if err != nil || !ok || current.AccessToken != "new-access" || current.RefreshToken != "new-refresh" || current.TokenRevision <= winners[0].TokenRevision || current.RefreshState != "ready" {
		t.Fatalf("rotated grant was not committed atomically: revision=%d state=%q ok=%v err=%v", current.TokenRevision, current.RefreshState, ok, err)
	}
	assertRepositoryTokenCiphertext(t, store, rotated)
	if err := store.RejectRepositoryConnection(ctx, connection); err != nil {
		t.Fatal(err)
	}
	statuses, err := store.RepositoryConnectionStatuses(ctx, current.User)
	if err != nil || statuses[current.Provider] != "connected" {
		t.Fatalf("stale rejection changed refreshed grant: %v %v", statuses, err)
	}
	if err := store.RejectRepositoryConnection(ctx, current); err != nil {
		t.Fatal(err)
	}
	statuses, err = store.RepositoryConnectionStatuses(ctx, current.User)
	if err != nil || statuses[current.Provider] != "reconnect_required" {
		t.Fatalf("rejected grant status: %v %v", statuses, err)
	}

	if err := store.DeleteRepositoryConnection(ctx, current.User, current.Provider); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertRepositoryConnection(ctx, rotated); err != nil {
		t.Fatal(err)
	}
	if err := store.RejectRepositoryConnection(ctx, current); err != nil {
		t.Fatal(err)
	}
	statuses, err = store.RepositoryConnectionStatuses(ctx, current.User)
	if err != nil || statuses[current.Provider] != "connected" {
		t.Fatalf("stale rejection changed the new connection: %v %v", statuses, err)
	}
}

func assertRepositoryTokenCiphertext(t *testing.T, store *Store, connection WorkerRepositoryConnection) {
	t.Helper()
	var access, refresh []byte
	var version string
	err := postgresTestPool(store).QueryRow(t.Context(), `SELECT access_token_ciphertext,
		refresh_token_ciphertext, token_key_version FROM krun_repository_connections
		WHERE user_id=$1 AND provider_id=$2`, connection.User, connection.Provider).
		Scan(&access, &refresh, &version)
	if err != nil {
		t.Fatal(err)
	}
	if len(access) == 0 || len(refresh) == 0 || version != "test" ||
		bytes.Contains(access, []byte(connection.AccessToken)) ||
		bytes.Contains(refresh, []byte(connection.RefreshToken)) {
		t.Fatal("repository credential was not stored as a versioned ciphertext")
	}
}
