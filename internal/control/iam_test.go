package control

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

// The service mutates a disposable working copy. A failed last-admin check must
// discard every change and leave existing sessions/permissions usable.
func TestIAMLastAdministratorFailureRollsBack(t *testing.T) {
	s, store := testServer(t)
	checkIAMLastAdministratorRollback(t, s, store)
}

func checkIAMLastAdministratorRollback(t *testing.T, s *Server, store *Store) {
	t.Helper()
	ctx := context.Background()
	snapshot := func() *State {
		var out *State
		err := store.View(ctx, PartitionIAM, func(st *State) error {
			var err error
			out, err = cloneState(st)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	before := snapshot()
	response := request(s, http.MethodPatch, "/api/v1/system/users/admin", "krun_cli_admin", `{"status":"disabled"}`)
	if response.Code != http.StatusConflict {
		t.Fatalf("disable last admin: %d %s", response.Code, response.Body.String())
	}
	var result struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.Error.Code != "LAST_ADMIN" {
		t.Fatalf("wrong error: %s", response.Body.String())
	}
	after := snapshot()
	if after.Users["admin"].Status != "active" || after.Users["admin"].AuthVersion != before.Users["admin"].AuthVersion || after.Revision != before.Revision {
		t.Fatal("failed IAM operation was committed")
	}
	response = request(s, http.MethodGet, "/api/v1/system/users", "krun_cli_admin", "")
	if response.Code != http.StatusOK {
		t.Fatalf("admin lost access after rollback: %d %s", response.Code, response.Body.String())
	}
}
