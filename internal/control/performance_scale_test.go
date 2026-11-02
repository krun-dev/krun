package control

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	appconfig "github.com/krun-dev/krun/internal/config"
)

// Opt-in capacity regression: an isolated schema avoids long, trigger-heavy
// row-by-row cleanup of a million-site fixture. This measures control-plane
// metadata reads, not public request throughput or a production capacity SLO.
func TestPostgresMillionSiteReadBound(t *testing.T) {
	raw := os.Getenv("KRUN_SCALE_SITES")
	if raw == "" {
		t.Skip("set KRUN_SCALE_SITES=1000000 for the metadata scale regression")
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1000 || n > 1000000 {
		t.Fatal("KRUN_SCALE_SITES must be between 1000 and 1000000")
	}
	dsn := os.Getenv("KRUN_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Fatal("isolated PostgreSQL test database required")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	name := "krun_scale_" + strconv.FormatInt(time.Now().UnixNano(), 10)
	identifier := pgx.Identifier{name}.Sanitize()
	if _, err = admin.Exec(ctx, `CREATE SCHEMA `+identifier); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = admin.Exec(context.Background(), `DROP SCHEMA `+identifier+` CASCADE`) }()
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := parsed.Query()
	q.Set("search_path", name)
	parsed.RawQuery = q.Encode()
	store, err := openStoreForTest(t, ctx, parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	started := time.Now()
	seedPerformanceSites(t, store, n)
	t.Logf("seeded %d sites with placements and production environments in %s", n, time.Since(started))
	samples := make([]time.Duration, 0, 25)
	for i := 0; i < 25; i++ {
		scope := stateReadScope{User: "actor", Organization: "tenant", Service: fmt.Sprintf("site-%06d", 2+2*i), Kind: "service", Limit: 100}
		started = time.Now()
		state, e := store.Open(withStateScope(ctx, scope), PartitionAll)
		if e != nil {
			t.Fatal(e)
		}
		samples = append(samples, time.Since(started))
		if len(state.Resources) != 1 || len(state.Workers.Services) != 1 || len(state.Workers.Placements) != 1 {
			t.Fatal("single-site query loaded unrelated sites")
		}
	}
	orgState, err := store.Open(withStateScope(ctx, stateReadScope{User: "actor", ActorOnly: true, AllOrganizations: true, Limit: 100}), PartitionIAM)
	if err != nil {
		t.Fatal(err)
	}
	if len(orgState.Resources) != 0 || len(orgState.Users) != 1 || len(orgState.Organizations) != 1 {
		t.Fatalf("organization navigation loaded unrelated state: resources=%d users=%d orgs=%d", len(orgState.Resources), len(orgState.Users), len(orgState.Organizations))
	}
	started = time.Now()
	config := appconfig.Config{DB: parsed.String(), Development: true, PublicURL: "http://127.0.0.1:4381", Providers: []appconfig.ProviderConfig{{ID: "local", Type: "dev", Source: "local", Name: "Development", Registration: "jit"}}}
	if _, err = NewServer(ctx, config, store); err != nil {
		t.Fatal(err)
	}
	startup := time.Since(started)
	loginState, err := store.Open(context.WithValue(ctx, loginReadScopeKey{}, loginReadScope{Source: "local", Subject: "actor"}), PartitionIAM)
	if err != nil {
		t.Fatal(err)
	}
	if len(loginState.Resources) != 0 || len(loginState.Users) != 1 {
		t.Fatal("login scope loaded the site catalogue")
	}
	t.Logf("startup/provider registration at %d sites=%s; login scope loaded 1 identity and 0 site resources", n, startup)
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	t.Logf("sites=%d single-site PostgreSQL read p50=%s p95=%s max=%s; loaded 1 site per request; organization navigation loaded 0 sites", n, samples[12], samples[23], samples[24])
}
