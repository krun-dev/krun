package control

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestPostgresDeviceApprovalAndConsumptionAreAtomic(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	device := Device{CodeHash: digest("postgres-device"), UserCode: "PGDEVICE", Expires: now().Add(time.Minute)}
	if err := store.insertDevice(ctx, device); err != nil {
		t.Fatal(err)
	}

	type approval struct {
		token string
		won   bool
		err   error
	}
	approvals := make(chan approval, 2)
	var wg sync.WaitGroup
	for _, token := range []string{"krun_cli_pg_one", "krun_cli_pg_two"} {
		wg.Add(1)
		go func(token string) {
			defer wg.Done()
			at := now()
			session := Session{User: token, Hash: digest(token), AuthVersion: 1, Created: at, Seen: at, Absolute: at.Add(time.Hour), CLI: true}
			won, err := store.approveDevice(ctx, device.CodeHash, token, session)
			approvals <- approval{token: token, won: won, err: err}
		}(token)
	}
	wg.Wait()
	close(approvals)
	winner := ""
	for result := range approvals {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.won {
			if winner != "" {
				t.Fatal("more than one device approval won")
			}
			winner = result.token
		}
	}
	if winner == "" {
		t.Fatal("no device approval won")
	}
	var sessionCount int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM krun_sessions WHERE session_hash IN ($1,$2)`, digest("krun_cli_pg_one"), digest("krun_cli_pg_two")).Scan(&sessionCount); err != nil {
		t.Fatal(err)
	}
	if sessionCount != 1 {
		t.Fatalf("losing approval left an orphan session: %d sessions", sessionCount)
	}

	type collection struct {
		token string
		won   bool
		err   error
	}
	collections := make(chan collection, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			token, won, err := store.consumeDevice(ctx, device.CodeHash)
			collections <- collection{token: token, won: won, err: err}
		}()
	}
	wg.Wait()
	close(collections)
	collected := 0
	for result := range collections {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.won {
			collected++
			if result.token != winner {
				t.Fatalf("poll returned %q, want approved token %q", result.token, winner)
			}
		}
	}
	if collected != 1 {
		t.Fatalf("device credential was collected %d times", collected)
	}
}
