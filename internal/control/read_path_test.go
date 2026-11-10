package control

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Track persisted row changes in the test's own schema. A no-op UPDATE
// from the session throttle does not fire a row trigger.
func TestGetRequestsDoNotWrite(t *testing.T) {
	s, store, base := workerTestServer(t)
	pool := postgresTestPool(store)
	observeTestWrites(t, pool)
	paths := []string{
		"/api/auth/me",
		"/api/v1/orgs",
		"/api/v1/orgs/org-a",
		"/api/v1/system/users",
		"/api/v1/orgs/org-a/members",
		"/api/v1/orgs/org-a/workers",
		"/api/v1/orgs/org-a/workers/svc-a",
		"/api/v1/orgs/org-a/workers/domains",
		"/api/v1/orgs/org-a/workers/repository-providers",
		base + "/environments",
		base + "/domains",
		base + "/repository",
		base + "/builds",
		base + "/operations",
		base + "/environments/production/history",
	}
	for _, path := range paths {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Origin", s.Config.PublicURL)
		r.Header.Set("Authorization", "Bearer krun_cli_admin")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Errorf("%s -> %d %s", path, w.Code, strings.TrimSpace(w.Body.String()))
		}
		var tables string
		if err := pool.QueryRow(t.Context(), "SELECT COALESCE(string_agg(DISTINCT table_name, ', '), '') FROM test_row_writes").Scan(&tables); err != nil {
			t.Fatal(err)
		}
		if tables != "" {
			t.Fatalf("GET %s wrote to %s", path, tables)
		}
	}
}

func observeTestWrites(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(t.Context(), `
		CREATE TABLE test_row_writes (table_name text NOT NULL);
		CREATE FUNCTION record_test_write() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			INSERT INTO test_row_writes VALUES (TG_TABLE_NAME);
			RETURN NULL;
		END;
		$$;
		DO $$
		DECLARE target record;
		BEGIN
			FOR target IN SELECT tablename FROM pg_tables
				WHERE schemaname = current_schema() AND tablename <> 'test_row_writes'
			LOOP
				EXECUTE format('CREATE TRIGGER test_row_write AFTER INSERT OR UPDATE OR DELETE ON %I
					FOR EACH ROW EXECUTE FUNCTION record_test_write()', target.tablename);
			END LOOP;
		END;
		$$;
	`)
	if err != nil {
		t.Fatal(err)
	}
}
