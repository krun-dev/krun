package authorization

import (
	"slices"
	"strings"
	"testing"

	"github.com/krun-dev/krun/internal/domain"
)

func TestThreeOrganizationRoles(t *testing.T) {
	roles, _, _ := Catalog()
	if !slices.Equal(roles, []string{"system_admin", "org_admin", "member", "viewer"}) {
		t.Fatalf("unexpected role catalog: %v", roles)
	}
	for _, role := range []string{"developer", "publisher", "system_admin", "unknown", ""} {
		if OrganizationRoleValid(role) {
			t.Errorf("invalid organization role accepted: %q", role)
		}
	}

	checks := []struct {
		action string
		kind   string
		id     string
		roles  string
	}{
		{"organization.read", "organization", "org-a", "org_admin member viewer"},
		{"organization.update", "organization", "org-a", "org_admin"},
		{"organization.delete", "organization", "org-a", "org_admin"},
		{"member.read", "organization", "org-a", "org_admin"},
		{"member.manage", "organization", "org-a", "org_admin"},
		{"iam.read", "organization", "org-a", "org_admin"},
		{"iam.grant", "organization", "org-a", "org_admin"},
		{"iam.revoke", "organization", "org-a", "org_admin"},
		{"audit.read", "organization", "org-a", "org_admin"},
		{"storage.bucket.create", "organization", "org-a", "org_admin member"},
		{"storage.bucket.update", "organization", "org-a", "org_admin member"},
		{"storage.object.read", "organization", "org-a", "org_admin member viewer"},
		{"storage.object.write", "organization", "org-a", "org_admin member"},
		{"storage.object.delete", "organization", "org-a", "org_admin member"},
		{"worker.list", "organization", "org-a", "org_admin member viewer"},
		{"worker.create", "organization", "org-a", "org_admin member"},
		{"worker.read", "worker", "svc-a", "org_admin member viewer"},
		{"worker.update", "worker", "svc-a", "org_admin member"},
		{"worker.delete", "worker", "svc-a", "org_admin member"},
		{"worker.version.read", "worker", "svc-a", "org_admin member viewer"},
		{"worker.version.create", "worker", "svc-a", "org_admin member"},
		{"worker.publish", "worker", "svc-a", "org_admin member"},
		{"worker.rollback", "worker", "svc-a", "org_admin member"},
		{"worker.invoke", "worker", "svc-a", "org_admin member"},
		{"build.start", "worker", "svc-a", "org_admin member"},
		{"log.read", "worker", "svc-a", "org_admin member viewer"},
		{"source.read", "worker", "svc-a", "org_admin member viewer"},
		{"binding.attach", "worker", "svc-a", "org_admin member"},
		{"binding.detach", "worker", "svc-a", "org_admin member"},
		{"model.list", "organization", "org-a", "org_admin member viewer"},
		{"model.create", "organization", "org-a", "org_admin member"},
		{"model.read", "model", "model-a", "org_admin member viewer"},
		{"model.update", "model", "model-a", "org_admin member"},
		{"model.delete", "model", "model-a", "org_admin member"},
		{"model.version.read", "model", "model-a", "org_admin member viewer"},
		{"model.version.create", "model", "model-a", "org_admin member"},
		{"model.publish", "model", "model-a", "org_admin member"},
		{"model.rollback", "model", "model-a", "org_admin member"},
		{"model.use", "model", "model-a", "org_admin member"},
		{"model.invoke", "model", "model-a", "org_admin member"},
		{"apiKey.create", "organization", "org-a", "org_admin member viewer"},
		{"apiKey.read", "api_key", "other-key", "org_admin"},
		{"apiKey.update", "api_key", "other-key", "org_admin"},
		{"apiKey.revoke", "api_key", "other-key", "org_admin"},
		{"organization.create", "system", "system", ""},
		{"system.configure", "system", "system", ""},
		{"system.user.read", "system", "system", ""},
		{"system.user.update", "system", "system", ""},
		{"system.role.read", "system", "system", ""},
		{"system.role.grant", "system", "system", ""},
		{"system.role.revoke", "system", "system", ""},
		{"audit.read", "system", "system", ""},
	}
	for _, role := range []string{"org_admin", "member", "viewer"} {
		t.Run(role, func(t *testing.T) {
			st, _, user := fixture()
			st.Assignments["viewer"] = Assignment{
				ID:          "viewer",
				SubjectKind: "user",
				SubjectID:   user.ID,
				Role:        role,
				ScopeKind:   "organization",
				ScopeID:     "org-a",
			}
			st.Resources["model-a"] = Resource{ID: "model-a", Kind: "model", Organization: "org-a"}
			st.Keys["other-key"] = APIKey{ID: "other-key", Owner: "someone-else", Organization: "org-a"}
			principal := ForReadSnapshot(st, Principal{User: user})
			for _, check := range checks {
				t.Run(check.action+"/"+check.kind, func(t *testing.T) {
					target, err := Resolve(st, check.kind, check.id, "production")
					if err != nil {
						t.Fatal(err)
					}
					decision, err := Authorize(st, principal, check.action, target)
					want := slices.Contains(strings.Fields(check.roles), role)
					if err != nil || decision.Allowed != want {
						t.Fatalf("allowed=%v, want %v: %v", decision.Allowed, want, err)
					}
				})
			}
		})
	}
}

func TestMemberScopeAndKeyRestrictions(t *testing.T) {
	st, _, user := fixture()
	st.Assignments["viewer"] = Assignment{
		ID:          "viewer",
		SubjectKind: "user",
		SubjectID:   user.ID,
		Role:        "member",
		ScopeKind:   "worker",
		ScopeID:     "svc-a",
		Conditions:  domain.Conditions{Environments: []string{"production"}},
	}
	key := APIKey{ID: "restricted", ScopeKind: "organization", Organization: "org-a", Policy: Policy{
		Version: 1,
		Statements: []Statement{{
			Effect:   "allow",
			Actions:  []string{"worker.read"},
			Resource: Selector{Kind: "worker", Organization: "org-a", IDs: []string{"svc-a"}},
		}},
	}}
	checks := []struct {
		name     string
		resource string
		env      string
		action   string
		key      *APIKey
		allowed  bool
	}{
		{"publish granted service", "svc-a", "production", "worker.publish", nil, true},
		{"no other environment", "svc-a", "development", "worker.publish", nil, false},
		{"no other organization", "svc-b", "production", "worker.publish", nil, false},
		{"read-only key can read", "svc-a", "production", "worker.read", &key, true},
		{"read-only key cannot publish", "svc-a", "production", "worker.publish", &key, false},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			target, err := Resolve(st, "worker", check.resource, check.env)
			if err != nil {
				t.Fatal(err)
			}
			decision, err := Authorize(st, Principal{User: user, Key: check.key}, check.action, target)
			if err != nil || decision.Allowed != check.allowed {
				t.Fatalf("decision=%+v, want %v: %v", decision, check.allowed, err)
			}
		})
	}
	delete(st.Members, domain.MemberKey("org-a", user.ID))
	target, _ := Resolve(st, "worker", "svc-a", "production")
	decision, err := Authorize(st, Principal{User: user}, "worker.publish", target)
	if err != nil || decision.Allowed {
		t.Fatalf("removed member retained access: %+v %v", decision, err)
	}
}
