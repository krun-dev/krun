package control

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestGetRequestsDoNotWrite enforces AGENTS.md §9.6: a read must not produce a
// write. It walks the GET routes the console polls and fails if any of them
// changes the loaded state.
//
// This catches a class of bug that is easy to reintroduce: returning a stored
// map or slice by reference and letting a "read-only" normaliser write into it.
func TestGetRequestsDoNotWrite(t *testing.T) {
	s, store, base := workerTestServer(t)
	writes := 0
	current := ""
	store.onPersist = func() {
		writes++
		t.Logf("write during GET %s", current)
	}
	defer func() { store.onPersist = nil }()
	paths := []string{
		"/api/auth/me",
		"/api/v1/me",
		"/api/v1/orgs",
		"/api/v1/orgs/org-a",
		"/api/v1/system/users",
		"/api/v1/orgs/org-a/members",
		"/api/v1/orgs/org-a/services",
		"/api/v1/orgs/org-a/services/svc-a",
		"/api/v1/orgs/org-a/workers/services",
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
		current = path
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Origin", s.Config.PublicURL)
		r.Header.Set("Authorization", "Bearer krun_cli_admin")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Errorf("%s -> %d %s", path, w.Code, strings.TrimSpace(w.Body.String()))
		}
	}
	if writes != 0 {
		t.Fatalf("GET requests performed %d state writes", writes)
	}
}
