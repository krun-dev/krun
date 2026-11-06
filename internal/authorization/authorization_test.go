package authorization

import (
	"testing"

	"github.com/krun-dev/krun/internal/domain"
)

type State = domain.State
type User = domain.User
type APIKey = domain.APIKey
type Organization = domain.Organization
type Member = domain.Member
type Assignment = domain.Assignment
type Resource = domain.Resource
type Group = domain.Group
type Policy = domain.Policy
type Statement = domain.Statement
type Selector = domain.Selector
type Conditions = domain.Conditions

var newState = domain.NewState
var memberKey = domain.MemberKey

func fixture() (*State, User, User) {
	st := newState()
	admin := User{ID: "admin", Source: "local", Subject: "a", Status: "active", AuthVersion: 1}
	viewer := User{ID: "viewer", Source: "local", Subject: "b", Status: "active", AuthVersion: 1}
	st.Users[admin.ID] = admin
	st.Users[viewer.ID] = viewer
	for _, id := range []string{"org-a", "org-b"} {
		st.Organizations[id] = Organization{ID: id, Name: id}
		st.Members[memberKey(id, admin.ID)] = Member{Organization: id, User: admin.ID}
	}
	st.Members[memberKey("org-a", viewer.ID)] = Member{Organization: "org-a", User: viewer.ID}
	st.Assignments["admin"] = Assignment{ID: "admin", SubjectKind: "user", SubjectID: admin.ID, Role: "system_admin", ScopeKind: "system", ScopeID: "system"}
	st.Assignments["viewer"] = Assignment{ID: "viewer", SubjectKind: "user", SubjectID: viewer.ID, Role: "viewer", ScopeKind: "organization", ScopeID: "org-a"}
	st.Resources["svc-a"] = Resource{ID: "svc-a", Kind: "worker", Organization: "org-a", Name: "A", Revision: 1}
	st.Resources["svc-b"] = Resource{ID: "svc-b", Kind: "worker", Organization: "org-b", Name: "B", Revision: 1}
	return st, admin, viewer
}
func TestAuthorizationBoundaries(t *testing.T) {
	st, admin, viewer := fixture()
	tests := []struct {
		name, user, action, kind, id, environment string
		allow                                     bool
	}{{"system admin cross organization", "admin", "worker.read", "worker", "svc-b", "global", true}, {"viewer reads own organization", "viewer", "worker.read", "worker", "svc-a", "global", true}, {"cross organization denied", "viewer", "worker.read", "worker", "svc-b", "global", false}, {"viewer cannot publish", "viewer", "worker.publish", "worker", "svc-a", "production", false}, {"unknown action denied", "admin", "worker.magic", "worker", "svc-a", "global", false}, {"action kind mismatch denied", "admin", "model.read", "worker", "svc-a", "global", false}, {"unknown environment denied", "admin", "worker.read", "worker", "svc-a", "bad env!", false}}
	_ = admin
	_ = viewer
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			target, e := Resolve(st, tc.kind, tc.id, tc.environment)
			if e != nil {
				t.Fatal(e)
			}
			d, e := Authorize(st, Principal{User: st.Users[tc.user]}, tc.action, target)
			if e != nil {
				t.Fatal(e)
			}
			if d.Allowed != tc.allow {
				t.Fatalf("decision %+v want %t", d, tc.allow)
			}
		})
	}
}
func TestKeyIntersectionExplicitDenyAndRevocation(t *testing.T) {
	st, admin, viewer := fixture()
	policy := Policy{Version: 1, Statements: []Statement{{Effect: "allow", Actions: []string{"worker.read", "worker.publish"}, Resource: Selector{Organization: "org-a", Kind: "worker", IDs: []string{"svc-a"}}, Conditions: Conditions{Environments: []string{"version-candidate"}}}, {Effect: "deny", Actions: []string{"worker.publish"}, Resource: Selector{Organization: "org-a", Kind: "worker", IDs: []string{"svc-a"}}}}}
	key := APIKey{ID: "key-a", ScopeKind: "organization", Organization: "org-a", Owner: admin.ID, Policy: policy}
	target, _ := Resolve(st, "worker", "svc-a", "version-candidate")
	p := Principal{User: admin, Key: &key}
	d, e := Authorize(st, p, "worker.read", target)
	if e != nil || !d.Allowed {
		t.Fatalf("%+v %v", d, e)
	}
	d, e = Authorize(st, p, "worker.publish", target)
	if e != nil || d.Allowed {
		t.Fatalf("explicit deny ignored %+v %v", d, e)
	}
	target.Environment = "production"
	d, e = Authorize(st, p, "worker.read", target)
	if e != nil || d.Allowed {
		t.Fatal("environment constraint ignored")
	}
	target.Environment = "version-candidate"
	p.User = viewer
	delete(st.Assignments, "viewer")
	d, e = Authorize(st, p, "worker.read", target)
	if e != nil || d.Allowed || !d.KeyAllowed {
		t.Fatalf("key bypassed current user permissions %+v %v", d, e)
	}
	target, _ = Resolve(st, "worker", "svc-b", "version-candidate")
	p.User = admin
	d, e = Authorize(st, p, "worker.read", target)
	if e != nil || d.Allowed {
		t.Fatal("key escaped organization")
	}
}
func TestGroupsAndIndependentSources(t *testing.T) {
	st, _, viewer := fixture()
	delete(st.Assignments, "viewer")
	st.Groups["operators"] = Group{ID: "operators", ScopeKind: "organization", ScopeID: "org-a", Members: []string{viewer.ID}}
	st.Assignments["publish"] = Assignment{ID: "publish", SubjectKind: "group", SubjectID: "operators", Role: "member", ScopeKind: "worker", ScopeID: "svc-a", Conditions: Conditions{Environments: []string{"version-candidate"}}}
	target, _ := Resolve(st, "worker", "svc-a", "version-candidate")
	d, e := Authorize(st, Principal{User: viewer}, "worker.publish", target)
	if e != nil || !d.Allowed {
		t.Fatalf("group scoped role failed %+v %v", d, e)
	}
	g := st.Groups["operators"]
	g.Members = nil
	st.Groups[g.ID] = g
	d, e = Authorize(st, Principal{User: viewer}, "worker.publish", target)
	if e != nil || d.Allowed {
		t.Fatal("removed group membership still authorized")
	}
	g.Members = []string{viewer.ID}
	st.Groups[g.ID] = g
	org := st.Organizations["org-a"]
	org.Sources = []string{"company"}
	st.Organizations[org.ID] = org
	d, e = Authorize(st, Principal{User: viewer}, "worker.publish", target)
	if e != nil || d.Allowed {
		t.Fatal("organization source gate bypassed")
	}
	other := viewer
	other.ID = "same-email-new-source"
	other.Source = "company"
	st.Users[other.ID] = other
	d, e = Authorize(st, Principal{User: other}, "worker.publish", target)
	if e != nil || d.Allowed {
		t.Fatal("new source inherited permissions")
	}
}
func TestPolicyDelegation(t *testing.T) {
	st, admin, viewer := fixture()
	tests := []struct {
		name   string
		p      Principal
		policy Policy
		ok     bool
	}{{"exact read within owner rights", Principal{User: viewer}, Policy{Version: 1, Statements: []Statement{{Effect: "allow", Actions: []string{"worker.read"}, Resource: Selector{Kind: "worker", Organization: "org-a", IDs: []string{"svc-a"}}}}}, true}, {"viewer can restrict a publish template", Principal{User: viewer}, Policy{Version: 1, Statements: []Statement{{Effect: "allow", Actions: []string{"worker.publish"}, Resource: Selector{Kind: "worker", Organization: "org-a", IDs: []string{"svc-a"}}}}}, true}, {"ordinary user organization wildcard", Principal{User: viewer}, Policy{Version: 1, Statements: []Statement{{Effect: "allow", Actions: []string{"worker.read"}, Resource: Selector{Kind: "worker", Organization: "org-a", All: true}}}}, true}, {"admin organization wildcard", Principal{User: admin}, Policy{Version: 1, Statements: []Statement{{Effect: "allow", Actions: []string{"worker.read"}, Resource: Selector{Kind: "worker", Organization: "org-a", All: true}}}}, true}, {"cross organization selector rejected", Principal{User: admin}, Policy{Version: 1, Statements: []Statement{{Effect: "allow", Actions: []string{"worker.read"}, Resource: Selector{Kind: "worker", Organization: "org-a", IDs: []string{"svc-b"}}}}}, false}, {"empty policy", Principal{User: admin}, Policy{Version: 1}, false}}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := ValidatePolicy(st, tc.p, "org-a", "organization", tc.policy)
			if (e == nil) != tc.ok {
				t.Fatalf("error=%v want valid=%t", e, tc.ok)
			}
		})
	}
}

func TestReadCacheDoesNotSurviveRevocation(t *testing.T) {
	st, _, viewer := fixture()
	p := ForReadSnapshot(st, Principal{User: viewer})
	target, _ := Resolve(st, "worker", "svc-a", "production")
	decision, err := Authorize(st, p, "worker.read", target)
	if err != nil || !decision.Allowed {
		t.Fatalf("initial grant: %+v %v", decision, err)
	}
	cached := p.cache.users[viewer.ID]
	if cached == nil {
		t.Fatal("read snapshot did not cache compiled policy")
	}
	if _, err = Authorize(st, p, "worker.read", target); err != nil || p.cache.users[viewer.ID] != cached {
		t.Fatal("read request recompiled policy")
	}

	// A fresh working copy with the same revision cannot use a prior request's cache.
	fresh, err := domain.CloneState(st)
	if err != nil {
		t.Fatal(err)
	}
	delete(fresh.Assignments, "viewer")
	decision, err = Authorize(fresh, p, "worker.read", target)
	if err != nil || decision.Allowed {
		t.Fatalf("cache escaped snapshot: %+v %v", decision, err)
	}

	// Mutation paths do not enable caching; a changed revision also invalidates it.
	delete(st.Assignments, "viewer")
	st.Revision++
	decision, err = Authorize(st, p, "worker.read", target)
	if err != nil || decision.Allowed {
		t.Fatalf("cache ignored revision: %+v %v", decision, err)
	}
}

func TestReadCachePreservesKeyCeiling(t *testing.T) {
	st, admin, _ := fixture()
	key := APIKey{ID: "key", ScopeKind: "organization", Organization: "org-a", Policy: Policy{Version: 1, Statements: []Statement{
		{Effect: "allow", Actions: []string{"worker.read"}, Resource: Selector{Kind: "worker", Organization: "org-a", IDs: []string{"svc-a"}}},
	}}}
	p := ForReadSnapshot(st, Principal{User: admin, Key: &key})
	target, _ := Resolve(st, "worker", "svc-a", "production")
	for _, action := range []string{"worker.read", "worker.publish", "worker.read"} {
		decision, err := Authorize(st, p, action, target)
		if err != nil || decision.Allowed != (action == "worker.read") {
			t.Fatalf("%s: %+v %v", action, decision, err)
		}
	}
	if p.cache.keys[key.ID] == nil {
		t.Fatal("key ceiling was not cached")
	}
}

func TestCatalogCannotChangeAuthorization(t *testing.T) {
	_, actions, environments := Catalog()
	actions["worker.read"][0] = "model"
	delete(actions, "organization.read")
	environments[0] = "bad env!"
	st, _, viewer := fixture()
	target, _ := Resolve(st, "worker", "svc-a", "global")
	decision, err := Authorize(st, Principal{User: viewer}, "worker.read", target)
	if err != nil || !decision.Allowed || EnvironmentValid("bad env!") {
		t.Fatalf("catalog mutated engine: %+v %v", decision, err)
	}
}
