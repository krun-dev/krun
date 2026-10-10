package control

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestUserWildcardKeysStayWithinCurrentPermissions(t *testing.T) {
	for _, scope := range []string{"organization", "system"} {
		t.Run(scope, func(t *testing.T) {
			s, store := testServer(t)
			selector := `{"kind":"*","all_in_system":true}`
			path := "/api/v1/system/api-keys"
			if scope == "organization" {
				selector = `{"organization_id":"org-a","kind":"*","all_in_organization":true}`
				path = "/api/v1/orgs/org-a/api-keys"
			}
			w := request(s, "POST", path, "krun_cli_viewer", `{"name":"wildcard","expires_in":0,"policy":{"schema_version":1,"statements":[{"effect":"allow","actions":["*"],"resource":`+selector+`,"conditions":{}}]}}`)
			if w.Code != 201 {
				t.Fatal(w.Code, w.Body.String())
			}
			var issued struct {
				ID, Key  string
				Expires  *time.Time `json:"expires_at"`
				Metadata APIKey     `json:"metadata"`
			}
			if e := json.Unmarshal(w.Body.Bytes(), &issued); e != nil || issued.Expires != nil || issued.Metadata.Expires != nil {
				t.Fatal("permanent expiry must be null", e)
			}
			update := `{"name":"renamed","expected_revision":1,"policy":{"schema_version":1,"statements":[{"effect":"allow","actions":["*"],"resource":` + selector + `,"conditions":{}}]}}`
			if request(s, "PATCH", "/api/v1/api-keys/"+issued.ID, "krun_cli_viewer", update).Code != 200 {
				t.Fatal("permanent key cannot update its policy")
			}
			if request(s, "GET", "/api/v1/orgs/org-a/services/svc-a", issued.Key, "").Code != 200 {
				t.Fatal("viewer wildcard cannot read its service")
			}
			if request(s, "PATCH", "/api/v1/orgs/org-a/services/svc-a", issued.Key, `{"name":"forbidden","expected_revision":1}`).Code != 403 {
				t.Fatal("wildcard escalated viewer to writer")
			}
			if request(s, "GET", "/api/v1/system/users", issued.Key, "").Code != 403 || request(s, "GET", "/api/v1/orgs/org-b/services/svc-b", issued.Key, "").Code != 404 {
				t.Fatal("wildcard escaped user permissions")
			}
			if request(s, "POST", "/api/v1/orgs/org-a/groups", issued.Key, `{"name":"forbidden"}`).Code != 403 {
				t.Fatal("wildcard key accessed session-only IAM")
			}
			if request(s, "GET", "/api/v1/api-keys/"+issued.ID, "krun_cli_viewer", "").Code != 200 || request(s, "GET", "/api/v1/api-keys/"+issued.ID, "krun_cli_admin", "").Code != 200 {
				t.Fatal("owner/admin cannot inspect key")
			}
			// Snapshot round trips must preserve nil deadlines and wildcard policies.
			_ = store.Update(context.Background(), func(st *State) error {
				st.Resources["future"] = Resource{ID: "future", Kind: "service", Organization: "org-a", Name: "future"}
				return nil
			})
			if request(s, "GET", "/api/v1/orgs/org-a/services/future", issued.Key, "").Code != 200 {
				t.Fatal("future resource not included in wildcard")
			}
			_ = store.Update(context.Background(), func(st *State) error { delete(st.Assignments, "viewer"); return nil })
			if request(s, "GET", "/api/v1/orgs/org-a/services/svc-a", issued.Key, "").Code != 404 {
				t.Fatal("revoked role remained usable through wildcard")
			}
			if request(s, "DELETE", "/api/v1/api-keys/"+issued.ID, "krun_cli_viewer", "").Code != 204 || request(s, "GET", "/api/v1/me", issued.Key, "").Code != 401 {
				t.Fatal("permanent key could not be revoked")
			}
		})
	}
}

func TestExactTemplatesUseLiveUserPermissions(t *testing.T) {
	st, _, viewer := fixture()
	policy := Policy{Version: 1, Statements: []Statement{{Effect: "allow", Actions: []string{"*"}, Resource: Selector{Kind: "service", Organization: "org-a", IDs: []string{"svc-a"}}}}}
	if e := validatePolicy(st, Principal{User: viewer}, "org-a", "organization", policy); e != nil {
		t.Fatal("ordinary user cannot select all actions", e)
	}
	key := APIKey{ID: "key", Organization: "org-a", ScopeKind: "organization", Policy: policy}
	target, _ := resolve(st, "service", "svc-a", "production")
	check := func(want bool) {
		t.Helper()
		d, e := Authorize(st, Principal{User: viewer, Key: &key}, "service.publish", target)
		if e != nil || d.Allowed != want {
			t.Fatal(d, e)
		}
	}
	check(false)
	st.Assignments["publisher"] = Assignment{ID: "publisher", SubjectKind: "user", SubjectID: viewer.ID, Role: "publisher", ScopeKind: "service", ScopeID: "svc-a"}
	check(true)
	org := st.Organizations["org-a"]
	org.Sources = []string{"another-source"}
	st.Organizations[org.ID] = org
	check(false)
	org.Sources = nil
	st.Organizations[org.ID] = org
	delete(st.Members, memberKey("org-a", viewer.ID))
	check(false)
}

func TestWildcardConditionsKindsAndDeny(t *testing.T) {
	st, admin, viewer := fixture()
	st.Resources["model-a"] = Resource{ID: "model-a", Kind: "model", Organization: "org-a"}
	policy := Policy{Version: 1, Statements: []Statement{
		{Effect: "allow", Actions: []string{"*"}, Resource: Selector{Kind: "service", AllSystem: true}, Conditions: Conditions{Environments: []string{"staging"}}},
		{Effect: "deny", Actions: []string{"*"}, Resource: Selector{Kind: "service", Organization: "org-a", IDs: []string{"svc-a"}}, Conditions: Conditions{Environments: []string{"production"}}},
	}}
	if e := validatePolicy(st, Principal{User: viewer}, "", "system", policy); e != nil {
		t.Fatal(e)
	}
	key := APIKey{ID: "key", ScopeKind: "system", Policy: policy}
	for _, tc := range []struct {
		kind, id, env string
		allowed       bool
	}{{"service", "svc-a", "staging", true}, {"service", "svc-a", "production", false}, {"model", "model-a", "staging", false}} {
		target, _ := resolve(st, tc.kind, tc.id, tc.env)
		d, e := Authorize(st, Principal{User: viewer, Key: &key}, tc.kind+".read", target)
		if e != nil || d.Allowed != tc.allowed {
			t.Fatalf("%+v: %+v %v", tc, d, e)
		}
	}
	model, _ := resolve(st, "model", "model-a", "staging")
	shared, err := Authorize(st, Principal{User: admin, Key: &key}, "log.read", model)
	if err != nil || !shared.UserAllowed || shared.KeyAllowed || shared.Allowed {
		t.Fatal("shared action escaped the key's selected resource kind", shared, err)
	}
	policy.Statements[0].Conditions = Conditions{}
	key.Policy = policy
	target, _ := resolve(st, "service", "svc-a", "production")
	d, e := Authorize(st, Principal{User: viewer, Key: &key}, "service.read", target)
	if e != nil || d.Allowed || !d.UserAllowed || d.KeyAllowed {
		t.Fatal("wildcard deny ignored", d, e)
	}
	if validatePolicy(st, Principal{User: viewer}, "org-a", "organization", policy) == nil {
		t.Fatal("organization key accepted system selector")
	}
	policy.Statements = []Statement{{Effect: "allow", Actions: []string{"model.read"}, Resource: Selector{Kind: "service", AllSystem: true}}}
	if validatePolicy(st, Principal{User: viewer}, "", "system", policy) == nil {
		t.Fatal("typed system wildcard accepted incompatible action")
	}
}

func TestKeyExpiryAndOwnerIsolation(t *testing.T) {
	s, store := testServer(t)
	for _, seconds := range []int{-1, 1, 59, 90*86400 + 1} {
		input, _ := json.Marshal(map[string]any{"name": "expiry", "expires_in": seconds, "policy": Policy{Version: 1, Statements: []Statement{{Effect: "allow", Actions: []string{"*"}, Resource: Selector{Kind: "*", AllSystem: true}}}}})
		if w := request(s, "POST", "/api/v1/system/api-keys", "krun_cli_viewer", string(input)); w.Code != 422 {
			t.Fatal("invalid expiry accepted", seconds, w.Code)
		}
	}
	w := request(s, "POST", "/api/v1/system/api-keys", "krun_cli_admin", `{"name":"finite","expires_in":60,"policy":{"schema_version":1,"statements":[{"effect":"allow","actions":["*"],"resource":{"kind":"*","all_in_system":true},"conditions":{}}]}}`)
	var issued struct {
		ID, Key  string
		Metadata APIKey
	}
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &issued) != nil || issued.Metadata.Expires == nil {
		t.Fatal(w.Code, "finite expiry missing")
	}
	if request(s, "GET", "/api/v1/api-keys/"+issued.ID, "krun_cli_viewer", "").Code != 404 {
		t.Fatal("ordinary user inspected another user's global key")
	}
	if w := request(s, "GET", "/api/v1/system/api-keys", "krun_cli_viewer", ""); w.Code != 200 || w.Body.String() == "" {
		t.Fatal("personal key list unavailable")
	} else {
		var list struct{ Items []APIKey }
		_ = json.Unmarshal(w.Body.Bytes(), &list)
		if len(list.Items) != 0 {
			t.Fatal("personal key list leaked another owner")
		}
	}
	_ = store.Update(context.Background(), func(st *State) error {
		key := st.Keys[issued.ID]
		past := now().Add(-time.Second)
		key.Expires = &past
		st.Keys[key.ID] = key
		return nil
	})
	if request(s, "GET", "/api/v1/me", issued.Key, "").Code != 401 {
		t.Fatal("expired key authenticated")
	}
	input := `{"name":"expired","expected_revision":1,"policy":{"schema_version":1,"statements":[{"effect":"allow","actions":["*"],"resource":{"kind":"*","all_in_system":true},"conditions":{}}]}}`
	if request(s, "PATCH", "/api/v1/api-keys/"+issued.ID, "krun_cli_admin", input).Code != 409 {
		t.Fatal("expired key updated")
	}
}
