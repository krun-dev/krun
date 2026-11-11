// Package testpgx owns the PostgreSQL test schema lifecycle.
package testpgx

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

// Schema is an isolated PostgreSQL schema created for one test. The DSN keeps
// the original connection settings and adds search_path=Name.
type Schema struct {
	Admin *pgxpool.Pool
	DSN   string
	Name  string
}

func NewSchema(t *testing.T, prefix string) *Schema {
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
	name := prefix + "_" + strconv.FormatInt(time.Now().UnixNano(), 10)
	identifier := pgx.Identifier{name}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(context.Background(), "DROP SCHEMA "+identifier+" CASCADE"); err != nil {
			t.Error(err)
		}
		admin.Close()
	})
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", name)
	parsed.RawQuery = query.Encode()
	return &Schema{Admin: admin, DSN: parsed.String(), Name: name}
}
