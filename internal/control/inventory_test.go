package control

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/krun-dev/krun/internal/domain"
)

func TestSystemInventoryAcrossOrganizations(t *testing.T) {
	s, store := testServer(t)
	const testKey = "krun_key_inventory_test"
	if err := store.Update(t.Context(), PartitionIAM, func(st *State) error {
		org := st.Organizations["org-b"]
		org.Sources = []string{"other-source"}
		st.Organizations[org.ID] = org
		for _, key := range []domain.APIKey{
			{ID: "key-a", Organization: "org-a", ScopeKind: "organization", Owner: "admin", Name: "Deploy", Prefix: "krun_key_example", Policy: domain.Policy{Version: 1}, Revision: 1},
			{ID: "key-b", Organization: "org-b", ScopeKind: "organization", Owner: "viewer", Name: "Backup", Revoked: true, Policy: domain.Policy{Version: 1}, Revision: 1},
			{ID: "key-c", ScopeKind: "system", Owner: "admin", Name: "Personal", Policy: domain.Policy{Version: 1}, Revision: 1},
		} {
			st.Keys[key.ID] = key
		}
		st.KeyHashes["key-c"] = digest(testKey)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(t.Context(), PartitionWorkers, func(st *State) error {
		st.Workers.Buckets["bucket-a"] = domain.WorkerBucket{
			ID: "bucket-a", Organization: "org-a", Name: "assets", Store: "internal-store", Enabled: true, Revision: 1, Created: now(),
		}
		st.Workers.Buckets["bucket-b"] = domain.WorkerBucket{
			ID: "bucket-b", Organization: "org-b", Name: "backups", Store: "internal-store", Revision: 1, Created: now(),
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	const base = "/api/v1/system/inventory/"
	const admin = "krun_cli_admin"
	for _, kind := range []string{"workers", "buckets", "api-keys"} {
		for token, want := range map[string]int{"": 401, "krun_cli_viewer": 403, testKey: 403} {
			if w := request(s, http.MethodGet, base+kind, token, ""); w.Code != want {
				t.Fatalf("%s: status %d, want %d: %s", kind, w.Code, want, w.Body.String())
			}
		}
	}

	readPage := func(path string) ([]map[string]any, string) {
		t.Helper()
		w := request(s, http.MethodGet, path, admin, "")
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status %d: %s", path, w.Code, w.Body.String())
		}
		for _, private := range []string{testKey, digest(testKey), "internal-store"} {
			if strings.Contains(w.Body.String(), private) {
				t.Fatalf("%s: returned private data", path)
			}
		}
		var page struct {
			Items []map[string]any `json:"items"`
			Next  string           `json:"next_after"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		return page.Items, page.Next
	}
	workers, next := readPage(base + "workers?limit=1")
	if len(workers) != 1 || workers[0]["id"] != "svc-a" || next != "svc-a" {
		t.Fatalf("unexpected first Workers page: %v, %q", workers, next)
	}
	workers, next = readPage(base + "workers?limit=1&after=" + next)
	if len(workers) != 1 || workers[0]["organization_id"] != "org-b" || next != "" {
		t.Fatalf("unexpected second Workers page: %v, %q", workers, next)
	}
	buckets, _ := readPage(base + "buckets?q=org-b")
	if len(buckets) != 1 || buckets[0]["id"] != "bucket-b" || buckets[0]["enabled"] != false {
		t.Fatalf("unexpected Buckets search: %v", buckets)
	}
	keys, _ := readPage(base + "api-keys")
	if len(keys) != 3 || keys[0]["prefix"] != "krun_key_example" ||
		keys[1]["organization_name"] != "org-b" || keys[2]["organization_id"] != nil {
		t.Fatalf("unexpected API keys: %v", keys)
	}
	keys, _ = readPage(base + "api-keys?status=revoked")
	if len(keys) != 1 || keys[0]["id"] != "key-b" || keys[0]["revoked"] != true {
		t.Fatalf("unexpected revoked keys: %v", keys)
	}
	keys, _ = readPage("/api/v1/system/api-keys")
	if len(keys) != 1 || keys[0]["id"] != "key-c" {
		t.Fatalf("unexpected keys on existing system endpoint: %v", keys)
	}
	w := request(s, http.MethodGet, "/api/v1/orgs/org-b/permissions", admin, "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("inventory access widened organization access: %d", w.Code)
	}
}
