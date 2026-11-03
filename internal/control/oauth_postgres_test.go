package control

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/storage"
)

func TestPostgresOAuthExchangesAreOneShotAndRevocable(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	at := now()
	a := domain.OAuthAuthorization{Hash: digest("authorization"), Client: "krun-cli", Scope: "krun:control offline_access", User: "actor", AuthVersion: 1, Decision: "approved", Created: at, Expires: at.Add(time.Minute), CodeHash: digest("code")}
	if err := store.InsertOAuthAuthorization(ctx, a); err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		grant   domain.OAuthGrant
		session Session
		refresh domain.OAuthRefresh
		err     error
	}
	results := make(chan outcome, 2)
	var wg sync.WaitGroup
	for i := range 2 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			suffix := fmt.Sprint(i)
			g := domain.OAuthGrant{ID: "grant-" + suffix, User: "actor", Client: "krun-cli", Scope: a.Scope, AuthVersion: 1, Created: at, Expires: at.Add(24 * time.Hour)}
			r := domain.OAuthRefresh{Hash: digest("refresh-" + suffix), Grant: g.ID, Created: at, Expires: g.Expires}
			session := Session{Hash: digest("access-" + suffix), User: g.User, CLI: true, OAuthGrant: g.ID, OAuthScope: g.Scope, AuthVersion: 1, Created: at, Seen: at, Absolute: at.Add(time.Hour)}
			results <- outcome{g, session, r, store.ExchangeOAuth(ctx, a.Hash, "", g, r, session)}
		}(i)
	}
	wg.Wait()
	close(results)
	var winner outcome
	wins := 0
	for result := range results {
		if result.err == nil {
			wins++
			winner = result
		} else if !errors.Is(result.err, storage.ErrOAuthGrant) {
			t.Fatal(result.err)
		}
	}
	if wins != 1 {
		t.Fatalf("code exchanged %d times", wins)
	}
	var sessions, grants int
	if err := postgresTestPool(store).QueryRow(ctx, `SELECT count(*) FROM krun_sessions`).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if err := postgresTestPool(store).QueryRow(ctx, `SELECT count(*) FROM krun_oauth_grants`).Scan(&grants); err != nil {
		t.Fatal(err)
	}
	if sessions != 1 || grants != 1 {
		t.Fatal("losing exchange left orphan credentials", sessions, grants)
	}
	if _, ok, err := store.SessionByHash(ctx, winner.session.Hash); err != nil || !ok {
		t.Fatal("issued session missing", err)
	}
	next := winner.refresh
	next.Hash = digest("next refresh")
	session := winner.session
	session.Hash = digest("next access")
	if err := store.ExchangeOAuth(ctx, "", winner.refresh.Hash, winner.grant, next, session); err != nil {
		t.Fatal(err)
	}
	if err := store.ExchangeOAuth(ctx, "", winner.refresh.Hash, winner.grant, next, session); !errors.Is(err, storage.ErrOAuthGrant) {
		t.Fatal("refresh replay accepted", err)
	}
	if id, err := store.OAuthGrantForToken(ctx, next.Hash, "krun-wrangler"); err != nil || id != "" {
		t.Fatal("cross-client revocation allowed", id, err)
	}
	if _, err := postgresTestPool(store).Exec(ctx, `UPDATE krun_sessions SET seen_at=now()-'40 minutes'::interval WHERE session_hash=$1`, session.Hash); err != nil {
		t.Fatal(err)
	}
	if err := store.RetainExpired(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.SessionByHash(ctx, session.Hash); err != nil || !ok {
		t.Fatal("OAuth access was pruned before its absolute expiry", err)
	}
	if err := store.RevokeOAuthGrant(ctx, winner.grant.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.SessionByHash(ctx, session.Hash); err != nil || ok {
		t.Fatal("revocation did not invalidate access", err)
	}
	if _, err := postgresTestPool(store).Exec(ctx, `UPDATE krun_oauth_grants SET created_at=now()-'2 days'::interval,expires_at=now()-'1 day'::interval`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := store.RetainExpired(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := postgresTestPool(store).QueryRow(ctx, `SELECT count(*) FROM krun_oauth_refresh_tokens`).Scan(&count); err != nil || count != 0 {
		t.Fatal("orphan refresh tokens not cleaned", count, err)
	}
}
