package storage

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/storage/models"
)

func TestORMModelsCoverTheCurrentSchema(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	if models.SchemaSHA256 != bytesDigest([]byte(schemaSQL)) {
		t.Fatal("ORM mappings are stale relative to the DDL")
	}
	rows, err := store.pool.Query(ctx, `SELECT table_name,column_name FROM information_schema.columns WHERE table_schema=current_schema() AND (table_name LIKE 'krun_%' OR table_name LIKE 'worker_%')`)
	if err != nil {
		t.Fatal(err)
	}
	actual := map[string]map[string]bool{}
	for rows.Next() {
		var table, column string
		if err := rows.Scan(&table, &column); err != nil {
			t.Fatal(err)
		}
		if actual[table] == nil {
			actual[table] = map[string]bool{}
		}
		actual[table][column] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, model := range models.All() {
		table := store.orm.Dialect().Tables().Get(reflect.TypeOf(model).Elem())
		columns, ok := actual[table.Name]
		if !ok {
			t.Fatalf("model refers to nonexistent table %s", table.Name)
		}
		for _, field := range table.Fields {
			if !columns[field.Name] {
				t.Errorf("model %s has unknown column %s", table.Name, field.Name)
			}
			delete(columns, field.Name)
		}
		if len(columns) > 0 {
			t.Errorf("unmapped columns for %s: %v", table.Name, columns)
		}
		delete(actual, table.Name)
	}
	if len(actual) > 0 {
		t.Fatalf("tables without ORM models: %v", actual)
	}
	var foreignKeys int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM pg_constraint WHERE contype='f' AND connamespace=current_schema()::regnamespace`).Scan(&foreignKeys); err != nil {
		t.Fatal(err)
	}
	if foreignKeys != 0 {
		t.Fatal("unexpected foreign-key constraints")
	}
}

func TestORMPersistsEmptyFalseAndNullValues(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	expiry := time.Now().UTC().Add(time.Hour)
	if err := store.Update(ctx, PartitionIAM, func(st *domain.State) error {
		st.Users["owner"] = domain.User{ID: "owner", Source: "test", Subject: "owner", Name: "old name", Status: "active", AuthVersion: 1}
		st.Organizations["org"] = domain.Organization{ID: "org", Name: "Organization", Sources: []string{"test"}, Revision: 1}
		st.Groups["group"] = domain.Group{ID: "group", ScopeKind: "organization", ScopeID: "org", Name: "Group", Members: []string{"owner"}, Revision: 1}
		st.Keys["key"] = domain.APIKey{ID: "key", Owner: "owner", Organization: "org", ScopeKind: "organization", Name: "old name", Prefix: "fixture", Policy: domain.Policy{Version: 1}, Expires: &expiry, Revoked: true, Revision: 1}
		st.KeyHashes["key"] = "fixture-hash"
		st.Revision++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(ctx, PartitionIAM, func(st *domain.State) error {
		user := st.Users["owner"]
		user.Name = ""
		st.Users[user.ID] = user
		key := st.Keys["key"]
		key.Name = ""
		key.Revoked = false
		key.Expires = nil
		key.Revision++
		st.Keys[key.ID] = key
		group := st.Groups["group"]
		group.Members = nil
		group.Revision++
		st.Groups[group.ID] = group
		st.Revision++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var key models.APIKey
	if err := store.orm.NewSelect().Model(&key).Where("key_id = ?", "key").Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if key.Name != "" || key.Revoked || key.ExpiresAt != nil || key.Revision != 2 {
		t.Fatalf("zero/null update was omitted: %+v", key)
	}
	var user models.User
	if err := store.orm.NewSelect().Model(&user).Where("user_id = ?", "owner").Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if user.Name != "" {
		t.Fatal("empty user name was omitted")
	}
	count, err := store.orm.NewSelect().Model((*models.GroupMember)(nil)).Where("group_id = ?", "group").Count(ctx)
	if err != nil || count != 0 {
		t.Fatalf("empty group membership did not remove rows: %d %v", count, err)
	}
}

func TestORMRollsBackEarlierWritesAfterLaterCASConflict(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	if err := store.Update(ctx, PartitionIAM, func(st *domain.State) error {
		st.Users["owner"] = domain.User{ID: "owner", Source: "test", Subject: "owner", Name: "before", Status: "active", AuthVersion: 1}
		st.Organizations["org"] = domain.Organization{ID: "org", Name: "before", Revision: 1}
		st.Revision++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	err := store.Update(ctx, PartitionIAM, func(st *domain.State) error {
		user := st.Users["owner"]
		user.Name = "must roll back"
		st.Users[user.ID] = user
		org := st.Organizations["org"]
		org.Name = "stale update"
		org.Revision++
		st.Organizations[org.ID] = org
		st.Revision++
		// A separate writer changes the row after this callback's snapshot. User
		// writes run first; the later organization CAS must roll both changes back.
		_, err := store.pool.Exec(ctx, `UPDATE krun_organizations SET name='concurrent',revision=revision+1 WHERE org_id='org'`)
		return err
	})
	var conflict *domain.APIError
	if !errors.As(err, &conflict) || conflict.Code != "REVISION_CONFLICT" {
		t.Fatalf("expected conflict, got %v", err)
	}
	var name string
	if err := store.pool.QueryRow(ctx, `SELECT name FROM krun_users WHERE user_id='owner'`).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "before" {
		t.Fatalf("earlier ORM write escaped rollback: %q", name)
	}
	if err := store.pool.QueryRow(ctx, `SELECT name FROM krun_organizations WHERE org_id='org'`).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "concurrent" {
		t.Fatalf("concurrent writer was overwritten: %q", name)
	}
}

func TestORMAndNativeQueriesShareThePool(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	at := time.Now().UTC()
	if err := store.InsertSession(ctx, "fixture", domain.Session{User: "actor", Created: at, Seen: at, Absolute: at.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.SessionByHash(ctx, digest("fixture")); err != nil || !ok {
		t.Fatalf("ORM session lookup: %t %v", ok, err)
	}
	var count int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM krun_sessions`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("native query: %d %v", count, err)
	}
	if stats := store.orm.DB.Stats(); stats.Idle != 0 || stats.InUse != 0 {
		t.Fatalf("ORM retained borrowed connections: %+v", stats)
	}
	if acquired := store.pool.Stat().AcquiredConns(); acquired != 0 {
		t.Fatalf("pool has %d borrowed connections", acquired)
	}
}
