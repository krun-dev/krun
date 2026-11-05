package control

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestOrganizationPermissions(t *testing.T) {
	s, store := testServer(t)
	path := "/api/v1/orgs/org-a/permissions"
	for _, tc := range []struct {
		name  string
		token string
		path  string
		code  int
	}{
		{"requires authentication", "", path, 401},
		{"viewer", "krun_cli_viewer", path, 200},
		{"system admin", "krun_cli_admin", path, 200},
		{"other organization is hidden", "krun_cli_viewer", "/api/v1/orgs/org-b/permissions", 404},
		{"unknown organization", "krun_cli_admin", "/api/v1/orgs/missing/permissions", 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := request(s, "GET", tc.path, tc.token, "")
			if response.Code != tc.code {
				t.Fatalf("got %d: %s", response.Code, response.Body.String())
			}
			if tc.code != 200 {
				return
			}
			var result struct {
				Organization string          `json:"organization_id"`
				Actions      map[string]bool `json:"actions"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.Organization != "org-a" || !result.Actions["service.list"] || !result.Actions["apiKey.create"] {
				t.Fatalf("missing user permissions: %+v", result)
			}
			for _, action := range []string{"member.read", "iam.read", "audit.read", "storage.object.read"} {
				if result.Actions[action] != (tc.token == "krun_cli_admin") {
					t.Errorf("unexpected %s permission: %+v", action, result.Actions)
				}
			}
			if _, exposed := result.Actions["system.user.read"]; exposed {
				t.Fatal("organization response included system permissions")
			}
		})
	}

	// Group grants must appear immediately, and disappear when revoked. Reads
	// must not append audits or change the metadata revision.
	audits, err := store.ReadAudits(context.Background(), time.Time{}, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	auditCount := len(audits)
	var revision int
	if err := store.Update(context.Background(), PartitionIAM, func(st *State) error {
		st.Groups["team"] = Group{
			ID:        "team",
			ScopeKind: "organization",
			ScopeID:   "org-a",
			Members:   []string{"viewer"},
		}
		st.Assignments["team-admin"] = Assignment{
			ID:          "team-admin",
			SubjectKind: "group",
			SubjectID:   "team",
			Role:        "org_admin",
			ScopeKind:   "organization",
			ScopeID:     "org-a",
		}
		revision = st.Revision
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, granted := range []bool{true, false} {
		response := request(s, "GET", path, "krun_cli_viewer", "")
		var result struct {
			Actions map[string]bool `json:"actions"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if response.Code != 200 || result.Actions["iam.read"] != granted {
			t.Fatalf("group grant=%v: %d %s", granted, response.Code, response.Body.String())
		}
		if err := store.Update(context.Background(), PartitionIAM, func(st *State) error {
			if st.Revision != revision || len(st.PendingAudits) != 0 {
				t.Error("permissions read modified state")
			}
			delete(st.Assignments, "team-admin")
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	audits, err = store.ReadAudits(context.Background(), time.Time{}, "", 100)
	if err != nil || len(audits) != auditCount {
		t.Fatalf("permissions reads appended audits: %d %v", len(audits), err)
	}
}

func TestOrganizationPermissionsRespectAPIKeyPolicy(t *testing.T) {
	s, _ := testServer(t)
	response := request(s, "POST", "/api/v1/orgs/org-a/api-keys", "krun_cli_admin", `{
		"name":"navigation test", "expires_in":3600,
		"policy":{"schema_version":1,"statements":[{
			"effect":"allow", "actions":["organization.read","service.list"],
			"resource":{"kind":"organization","organization_id":"org-a","ids":["org-a"]},
			"conditions":{}
		}]}
	}`)
	if response.Code != 201 {
		t.Fatal(response.Code, response.Body.String())
	}
	var key struct{ Key string }
	if err := json.Unmarshal(response.Body.Bytes(), &key); err != nil {
		t.Fatal(err)
	}
	response = request(s, "GET", "/api/v1/orgs/org-a/permissions", key.Key, "")
	var result struct {
		Actions map[string]bool `json:"actions"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 || !result.Actions["service.list"] || result.Actions["iam.read"] || result.Actions["member.read"] {
		t.Fatalf("key policy was bypassed: %d %s", response.Code, response.Body.String())
	}
}
