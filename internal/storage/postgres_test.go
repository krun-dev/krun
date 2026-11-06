package storage

import (
	"context"
	"net/url"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func postgresTestStore(t *testing.T) *Store {
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
	name := "krun_storage_" + strconv.FormatInt(time.Now().UnixNano(), 10)
	identifier := pgx.Identifier{name}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+identifier+" CASCADE") })
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", name)
	parsed.RawQuery = query.Encode()
	store, err := Open(ctx, parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	return store
}
func TestPostgresUsesSingleSchemaBaseline(t *testing.T) {
	dsn := os.Getenv("KRUN_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set KRUN_TEST_POSTGRES_URL for PostgreSQL integration")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := "krun_baseline_" + strconv.FormatInt(time.Now().UnixNano(), 10)
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, `CREATE SCHEMA `+identifier); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = admin.Exec(context.Background(), `DROP SCHEMA `+identifier+` CASCADE`) }()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := u.Query()
	query.Set("search_path", schema)
	u.RawQuery = query.Encode()
	store, err := Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var baseline string
	if err := store.pool.QueryRow(ctx, `SELECT value FROM krun_meta WHERE key='schema_baseline'`).Scan(&baseline); err != nil || baseline != bytesDigest([]byte(schemaSQL)) {
		t.Fatalf("baseline=%q err=%v", baseline, err)
	}
	reopened, err := Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	reopened.Close()
	var poolColumns, placementTables int
	if err := store.pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM information_schema.columns
		 WHERE table_schema=current_schema()
		   AND table_name IN ('krun_workers','krun_worker_versions')
		   AND column_name='pool'),
		(SELECT count(*) FROM information_schema.tables
		 WHERE table_schema=current_schema() AND table_name='krun_worker_placements')`).Scan(
		&poolColumns, &placementTables); err != nil {
		t.Fatal(err)
	}
	if poolColumns != 0 || placementTables != 1 {
		t.Fatalf("baseline model: pool columns=%d placement tables=%d", poolColumns, placementTables)
	}
}

// truncateControlTables empties every control-plane table. Sessions, browsers
// and login transactions are included because rate limits and expiry are
// checked against them. A closed pool is not an error: tests may restart the
// store themselves.

func TestOpenStoreRequiresPostgres(t *testing.T) {
	if _, err := Open(context.Background(), ""); err == nil {
		t.Fatal("store without PostgreSQL accepted")
	}
}

func TestPostgresReconnectsBeforeMutation(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	conn, err := store.pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	pid := conn.Conn().PgConn().PID()
	conn.Release()
	killer, err := pgx.Connect(ctx, os.Getenv("KRUN_TEST_POSTGRES_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer killer.Close(ctx)
	var terminated bool
	if err = killer.QueryRow(ctx, "SELECT pg_terminate_backend($1)", pid).Scan(&terminated); err != nil || !terminated {
		t.Fatal("terminate pooled connection", err)
	}
	calls := 0
	if err = store.Update(ctx, PartitionAll, func(st *State) error { calls++; st.Revision++; return nil }); err != nil {
		t.Fatal("failed to replace terminated connection", err)
	}
	if calls != 1 {
		t.Fatal("state mutation retried")
	}
}

// Existing application data must never be implicitly migrated or erased.

func TestPostgresSchemaBaselineMismatchPreservesData(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	if _, err := store.pool.Exec(ctx, `INSERT INTO krun_users(user_id,source,subject,name,status) VALUES('baseline-survivor','local','baseline-survivor','Survivor','active')`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = store.pool.Exec(ctx, `INSERT INTO krun_meta(key,value) VALUES('schema_baseline',$1) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, bytesDigest([]byte(schemaSQL)))
	})
	for _, missing := range []bool{false, true} {
		query := `UPDATE krun_meta SET value='different-baseline' WHERE key='schema_baseline'`
		if missing {
			query = `DELETE FROM krun_meta WHERE key='schema_baseline'`
		}
		if _, err := store.pool.Exec(ctx, query); err != nil {
			t.Fatal(err)
		}
		rejected, err := Open(ctx, store.pool.Config().ConnString())
		if rejected != nil {
			rejected.Close()
		}
		if err == nil {
			t.Fatal("incompatible baseline accepted")
		}
		var count int
		if err = store.pool.QueryRow(ctx, `SELECT count(*) FROM krun_users WHERE user_id='baseline-survivor'`).Scan(&count); err != nil || count != 1 {
			t.Fatalf("existing row changed: count=%d err=%v", count, err)
		}
	}
}
