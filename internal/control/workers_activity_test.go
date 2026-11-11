package control

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestWorkerActivityTracksVisibleWorkWithoutHistory(t *testing.T) {
	s, store, base := workerTestServer(t)
	terminal := seedWorkerBuild(t, s, store, base)
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := store.Update(t.Context(), PartitionWorkers, func(st *State) error {
		st.Workers.Builds["build-production"] = WorkerBuild{
			ID: "build-production", Organization: "org-a", Worker: "svc-a",
			Environment: "production", State: "queued", Created: created,
		}
		st.Workers.Operations["op-preview"] = WorkerOperation{
			ID: "op-preview", Organization: "org-a", Worker: "svc-a",
			Environment: "fix-demo", State: "validating", Created: created,
			Steps: []WorkerStep{{Name: "candidate", State: "running"}},
		}
		for i := 0; i < 100; i++ {
			id := fmt.Sprintf("op-terminal-%03d", i)
			st.Workers.Operations[id] = WorkerOperation{
				ID: id, Organization: "org-a", Worker: "svc-a",
				Environment: "production", State: "available", Created: created.Add(time.Minute),
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	issue := request(s, http.MethodPost, "/api/v1/orgs/org-a/api-keys", "krun_cli_admin", `{
		"name":"preview activity","expires_in":3600,"policy":{"schema_version":1,"statements":[{
			"effect":"allow","actions":["worker.read","worker.version.read"],
			"resource":{"kind":"worker","organization_id":"org-a","ids":["svc-a"]},
			"conditions":{"environments":["fix-demo"]}
		}]}
	}`)
	var preview struct{ Key string }
	if issue.Code != http.StatusCreated || json.Unmarshal(issue.Body.Bytes(), &preview) != nil || preview.Key == "" {
		t.Fatal(issue.Code, issue.Body.String())
	}

	type activity struct {
		Active bool   `json:"active"`
		Token  string `json:"token"`
	}
	checkActivity := func(key string, active bool) activity {
		t.Helper()
		poll := request(s, http.MethodGet, base+"/activity", key, "")
		overview := request(s, http.MethodGet, base+"/overview", key, "")
		if poll.Code != http.StatusOK || overview.Code != http.StatusOK {
			t.Fatalf("activity status=%d body=%s; overview status=%d", poll.Code, poll.Body.String(), overview.Code)
		}
		t.Logf("activity response = %d bytes, overview = %d bytes", poll.Body.Len(), overview.Body.Len())
		var compact activity
		var full struct {
			Activity activity `json:"activity"`
		}
		if err := json.Unmarshal(poll.Body.Bytes(), &compact); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(overview.Body.Bytes(), &full); err != nil {
			t.Fatal(err)
		}
		if compact.Active != active || compact.Token == "" || compact != full.Activity {
			t.Fatalf("activity = %+v, overview activity = %+v, want active=%t", compact, full.Activity, active)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(poll.Body.Bytes(), &fields); err != nil || len(fields) != 2 {
			t.Fatalf("activity returned more than active and token: %s", poll.Body)
		}
		return compact
	}
	adminBefore := checkActivity("krun_cli_admin", true)
	previewBefore := checkActivity(preview.Key, true)

	scoped := httptest.NewRequest(http.MethodGet, base+"/activity", nil)
	scoped.Pattern = "GET /api/v1/orgs/{org}/workers/{resource}/activity"
	scoped.SetPathValue("org", "org-a")
	scoped.SetPathValue("resource", "svc-a")
	scoped, err := s.scopedRequest(scoped, authCredentials{session: Session{User: "admin"}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Open(scoped.Context(), PartitionAll)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Workers.Builds[terminal.ID].ID != "" || len(snapshot.Workers.Builds) != 1 ||
		len(snapshot.Workers.Operations) != 1 || len(snapshot.Workers.Artifacts) != 0 {
		t.Fatalf("activity read included terminal or artifact rows: builds=%d operations=%d artifacts=%d",
			len(snapshot.Workers.Builds), len(snapshot.Workers.Operations), len(snapshot.Workers.Artifacts))
	}
	if err := store.FinishBuild(t.Context(), "build-production", "failed", "test failure", "", ""); err != nil {
		t.Fatal(err)
	}
	if after := checkActivity("krun_cli_admin", true); after.Token == adminBefore.Token {
		t.Fatal("completion did not invalidate the admin overview")
	}
	if after := checkActivity(preview.Key, true); after != previewBefore {
		t.Fatal("hidden build completion changed the preview token")
	}
	claim, owned, err := store.ClaimOperation(t.Context(), "op-preview", "replica")
	if err != nil || !owned {
		t.Fatalf("claim: owned=%t err=%v", owned, err)
	}
	beforeNote := checkActivity(preview.Key, true)
	if err := store.NoteClaimedOperation(t.Context(), claim, "waiting for candidate"); err != nil {
		t.Fatal(err)
	}
	if after := checkActivity(preview.Key, true); after.Token == beforeNote.Token {
		t.Fatal("progress update did not invalidate the overview")
	}
	if err := store.FailClaimedOperation(t.Context(), claim, "test failure"); err != nil {
		t.Fatal(err)
	}
	checkActivity(preview.Key, false)
}
