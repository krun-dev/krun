package control

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/krun-dev/krun/internal/domain"
)

func TestCloudflareVersionListsKeepTheirProtocolWhenConsolePages(t *testing.T) {
	server, store, cfBase := cfTestServer(t)
	active := seedPublishedWorker(t, store)
	created := time.Now().UTC()
	if err := store.Update(context.Background(), PartitionWorkers, func(st *State) error {
		for index := 0; index < 105; index++ {
			version := active
			version.ID = fmt.Sprintf("cf-version-%03d", index)
			version.Created = created.Add(time.Duration(index) * time.Second)
			version.CreatedBy = "admin"
			version.CreatedByName = "Release Author"
			st.Workers.Versions[version.ID] = version

			deployment := domain.WorkerDeployment{
				ID:           fmt.Sprintf("cf-deployment-%03d", index),
				Organization: version.Organization,
				Worker:       version.Worker,
				Environment:  version.Environment,
				Version:      version.ID,
				Operation:    fmt.Sprintf("cf-operation-%03d", index),
				Created:      version.Created,
				Expires:      created.Add(time.Hour),
			}
			st.Workers.Deployments[deployment.ID] = deployment
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := postgresTestPool(store).Exec(t.Context(), "UPDATE krun_worker_deployments SET committed_at=created_at WHERE worker_id=$1", active.Worker); err != nil {
		t.Fatal(err)
	}

	for _, endpoint := range []string{"versions", "deployments"} {
		response := request(server, http.MethodGet, "/api/v1/orgs/org-a/workers/svc-a/"+endpoint+"?limit=1", "krun_cli_admin", "")
		var native struct {
			Items []json.RawMessage `json:"items"`
			Next  string            `json:"next_after"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &native); err != nil || response.Code != http.StatusOK || len(native.Items) != 1 || native.Next == "" {
			t.Fatalf("native %s pagination: %d %s", endpoint, response.Code, response.Body.String())
		}

		// Console-only search, limit and cursor parameters must not truncate
		// Wrangler's version/deployment lists or change its result envelope.
		response = request(server, http.MethodGet, cfBase+"/"+endpoint+"?q=missing&limit=1&after=invalid", "krun_cli_admin", "")
		var cf struct {
			Success bool                       `json:"success"`
			Result  map[string]json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &cf); err != nil || response.Code != http.StatusOK || !cf.Success {
			t.Fatalf("CF %s envelope: %d %s", endpoint, response.Code, response.Body.String())
		}
		if _, exists := cf.Result["next_after"]; exists {
			t.Fatalf("console cursor leaked into CF %s", endpoint)
		}
		itemKey := "items"
		if endpoint == "deployments" {
			itemKey = "deployments"
		}
		var items []map[string]json.RawMessage
		if err := json.Unmarshal(cf.Result[itemKey], &items); err != nil {
			t.Fatal(err)
		}
		want := 105
		if endpoint == "versions" {
			want++ // The original published version is also visible.
		}
		if len(items) != want {
			t.Fatalf("CF %s truncated: got %d, want %d", endpoint, len(items), want)
		}
		if endpoint == "versions" && (items[0]["metadata"] == nil || items[0]["resources"] == nil || items[0]["annotations"] == nil) {
			t.Fatalf("CF version representation changed: %s", cf.Result[itemKey])
		}
		if endpoint == "deployments" && (items[0]["strategy"] == nil || items[0]["versions"] == nil || items[0]["created_on"] == nil) {
			t.Fatalf("CF deployment representation changed: %s", cf.Result[itemKey])
		}
	}

	for _, endpoint := range []string{"versions/cf-version-000", "deployments/cf-deployment-000"} {
		response := request(server, http.MethodGet, cfBase+"/"+endpoint, "krun_cli_admin", "")
		if response.Code != http.StatusOK {
			t.Fatalf("CF off-page detail %s: %d %s", endpoint, response.Code, response.Body.String())
		}
	}
}
