package control

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"testing"
)

func grantOrganizationRole(t *testing.T, server *Server, role string) Assignment {
	t.Helper()
	input := fmt.Sprintf(`{
		"subject_kind":"user",
		"subject_id":"viewer",
		"role_id":%q,
		"scope_kind":"organization",
		"scope_id":"org-a",
		"conditions":{}
	}`, role)
	response := request(server, http.MethodPost, "/api/v1/orgs/org-a/role-assignments", "krun_cli_admin", input)
	if response.Code != http.StatusCreated {
		t.Fatalf("grant %s: %d %s", role, response.Code, response.Body.String())
	}
	var assignment Assignment
	if err := json.Unmarshal(response.Body.Bytes(), &assignment); err != nil {
		t.Fatal(err)
	}
	return assignment
}

func TestOrganizationRoleHTTPBoundaries(t *testing.T) {
	for _, role := range []string{"org_admin", "member", "viewer"} {
		t.Run(role, func(t *testing.T) {
			server, _ := testServer(t)
			grantOrganizationRole(t, server, role)
			writeStatus := http.StatusForbidden
			managementStatus := http.StatusForbidden
			if role != "viewer" {
				writeStatus = http.StatusCreated
			}
			if role == "org_admin" {
				managementStatus = http.StatusOK
			}
			checks := []struct {
				method string
				path   string
				body   string
				status int
			}{
				{"GET", "/api/v1/orgs/org-a/workers/svc-a", "", http.StatusOK},
				{"POST", "/api/v1/orgs/org-a/workers", `{"name":"role-resource"}`, writeStatus},
				{"GET", "/api/v1/orgs/org-a/members", "", managementStatus},
				{"GET", "/api/v1/orgs/org-a/role-assignments", "", managementStatus},
				{"GET", "/api/v1/orgs/org-a/audit", "", managementStatus},
				{"GET", "/api/v1/system/users", "", http.StatusForbidden},
				{"GET", "/api/v1/system/audit", "", http.StatusForbidden},
				{"GET", "/api/v1/orgs/org-b/members", "", http.StatusForbidden},
				{"POST", "/api/v1/orgs/org-b/workers", `{"name":"cross-org"}`, http.StatusForbidden},
			}
			for _, check := range checks {
				response := request(server, check.method, check.path, "krun_cli_viewer", check.body)
				if response.Code != check.status {
					t.Errorf("%s %s: got %d, want %d: %s", check.method, check.path, response.Code, check.status, response.Body.String())
				}
			}
			if role != "org_admin" {
				response := request(server, "PATCH", "/api/v1/orgs/org-a", "krun_cli_viewer", `{"name":"cannot-edit","expected_revision":0,"allowed_sources":[]}`)
				if response.Code != http.StatusForbidden {
					t.Fatalf("ordinary role can edit organization: %d %s", response.Code, response.Body.String())
				}
				response = request(server, "POST", "/api/v1/orgs/org-a/role-assignments", "krun_cli_viewer", `{"subject_kind":"user","subject_id":"viewer","role_id":"org_admin","scope_kind":"organization","scope_id":"org-a","conditions":{}}`)
				if response.Code != http.StatusForbidden {
					t.Fatalf("ordinary role can escalate privileges: %d %s", response.Code, response.Body.String())
				}
			}
		})
	}
}

func TestRoleCatalogRejectsRemovedRoles(t *testing.T) {
	server, _ := testServer(t)
	response := request(server, "GET", "/api/v1/roles", "krun_cli_viewer", "")
	var catalog struct {
		Roles []string `json:"roles"`
	}
	if response.Code != http.StatusOK {
		t.Fatalf("role catalog: %d %s", response.Code, response.Body.String())
	}
	if err := json.Unmarshal(response.Body.Bytes(), &catalog); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(catalog.Roles, []string{"system_admin", "org_admin", "member", "viewer"}) {
		t.Fatalf("unexpected catalog: %d %s", response.Code, response.Body.String())
	}
	for _, role := range []string{"developer", "publisher"} {
		input := fmt.Sprintf(`{"subject_kind":"user","subject_id":"viewer","role_id":%q,"scope_kind":"organization","scope_id":"org-a","conditions":{}}`, role)
		response := request(server, "POST", "/api/v1/orgs/org-a/role-assignments", "krun_cli_admin", input)
		if response.Code != http.StatusUnprocessableEntity {
			t.Fatalf("removed role %s accepted: %d %s", role, response.Code, response.Body.String())
		}
	}
}

func TestRoleChangesRestrictExistingAPIKey(t *testing.T) {
	server, _ := testServer(t)
	grant := grantOrganizationRole(t, server, "member")
	response := request(server, "POST", "/api/v1/orgs/org-a/api-keys", "krun_cli_viewer", `{
		"name":"resource-access", "expires_in":3600,
		"policy":{"schema_version":1,"statements":[{
			"effect":"allow", "actions":["*"],
			"resource":{"kind":"*","organization_id":"org-a","all_in_organization":true},
			"conditions":{}
		}]}
	}`)
	var key struct{ Key string }
	if response.Code != http.StatusCreated || json.Unmarshal(response.Body.Bytes(), &key) != nil {
		t.Fatalf("create key: %d %s", response.Code, response.Body.String())
	}
	response = request(server, "POST", "/api/v1/orgs/org-a/workers", key.Key, `{"name":"member-can-create"}`)
	if response.Code != http.StatusCreated {
		t.Fatalf("member key: %d %s", response.Code, response.Body.String())
	}
	response = request(server, "DELETE", "/api/v1/orgs/org-a/role-assignments/"+grant.ID, "krun_cli_admin", "")
	if response.Code != http.StatusNoContent {
		t.Fatalf("revoke member role: %d %s", response.Code, response.Body.String())
	}
	response = request(server, "POST", "/api/v1/orgs/org-a/workers", key.Key, `{"name":"viewer-cannot-create"}`)
	if response.Code != http.StatusForbidden {
		t.Fatalf("existing key retained revoked write permission: %d %s", response.Code, response.Body.String())
	}
	response = request(server, "GET", "/api/v1/orgs/org-a/workers/svc-a", key.Key, "")
	if response.Code != http.StatusOK {
		t.Fatalf("viewer key lost read access: %d %s", response.Code, response.Body.String())
	}
}

func TestMemberCanBuildCreateVersionPublishAndRollback(t *testing.T) {
	server, store, base := workerTestServer(t)
	grantOrganizationRole(t, server, "member")
	payload, err := json.Marshal(map[string]any{
		"environment": "fix-demo",
		"entrypoint":  "index.ts",
		"files": map[string]string{
			"index.ts": `export default {
  fetch() {
    return new Response("member");
  }
};
`,
		},
		"idempotency_key": "member-build",
	})
	if err != nil {
		t.Fatal(err)
	}
	response := request(server, "POST", base+"/builds", "krun_cli_viewer", string(payload))
	if response.Code != http.StatusAccepted {
		t.Fatalf("member build: %d %s", response.Code, response.Body.String())
	}
	activeVersion := seedPublishedWorker(t, store)
	response = request(server, "POST", base+"/environments/production/versions", "krun_cli_viewer", `{"artifact_id":"art_secret","idempotency_key":"member-version"}`)
	var version WorkerVersion
	if response.Code != http.StatusCreated || json.Unmarshal(response.Body.Bytes(), &version) != nil {
		t.Fatalf("member version: %d %s", response.Code, response.Body.String())
	}
	var revision int
	if err := store.View(context.Background(), PartitionWorkers, func(st *State) error {
		revision = st.Workers.Workers["svc-a"].Environments["production"].Revision
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	publish := fmt.Sprintf(`{"version_id":%q,"expected_revision":%d,"idempotency_key":"member-publish"}`, version.ID, revision)
	response = request(server, "POST", base+"/environments/production/deployments", "krun_cli_viewer", publish)
	if response.Code != http.StatusAccepted {
		t.Fatalf("member publish: %d %s", response.Code, response.Body.String())
	}
	var operation WorkerOperation
	if err := json.Unmarshal(response.Body.Bytes(), &operation); err != nil {
		t.Fatal(err)
	}
	// Model the controller's completed publish before requesting a rollback.
	if err := store.Update(context.Background(), PartitionWorkers, func(st *State) error {
		service := st.Workers.Workers["svc-a"]
		environment := service.Environments["production"]
		environment.Version = version.ID
		environment.Confirmed = version.ID
		environment.Revision = revision + 1
		service.Environments["production"] = environment
		st.Workers.Workers["svc-a"] = service
		operation.State = "committed"
		st.Workers.Operations[operation.ID] = operation
		st.Workers.Histories["svc-a/production"] = []WorkerHistory{{Revision: revision, Version: activeVersion.ID}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	rollback := fmt.Sprintf(`{"version_id":%q,"expected_revision":%d,"idempotency_key":"member-rollback"}`, activeVersion.ID, revision+1)
	response = request(server, "POST", base+"/environments/production/rollbacks", "krun_cli_viewer", rollback)
	if response.Code != http.StatusAccepted {
		t.Fatalf("member rollback: %d %s", response.Code, response.Body.String())
	}
}
