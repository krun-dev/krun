package iam_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/krun-dev/krun/internal/authorization"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/iam"
	"github.com/krun-dev/krun/internal/securetoken"
)

func fixture() (iam.Service, *domain.State, domain.User) {
	svc := iam.NewService(func(source string) bool { return source == "local" })
	st := domain.NewState()
	user := domain.User{ID: "member", Source: "local", Status: "active", AuthVersion: 1}
	st.Users[user.ID] = user
	for _, org := range []string{"a", "b"} {
		st.Organizations[org] = domain.Organization{ID: org, Name: org, Revision: 1}
		st.Members[domain.MemberKey(org, user.ID)] = domain.Member{Organization: org, User: user.ID}
		st.Resources["service-"+org] = domain.Resource{ID: "service-" + org, Kind: "worker", Organization: org, Revision: 1}
		st.Groups["group-"+org] = domain.Group{ID: "group-" + org, ScopeKind: "organization", ScopeID: org, Members: []string{user.ID}, Revision: 1}
		st.Assignments["grant-"+org] = domain.Assignment{ID: "grant-" + org, SubjectKind: "group", SubjectID: "group-" + org, Role: "viewer", ScopeKind: "organization", ScopeID: org, Revision: 1}
	}
	return svc, st, user
}
func target(org string) authorization.Target {
	return authorization.Target{Kind: "organization", ID: org, Organization: org, Environment: "global"}
}
func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	var problem *domain.APIError
	if !errors.As(err, &problem) || problem.Code != code {
		t.Fatalf("error = %v; want %s", err, code)
	}
}

func TestMemberRemovalRevokesOnlyItsOrganization(t *testing.T) {
	svc, st, user := fixture()
	st.Assignments["direct"] = domain.Assignment{ID: "direct", SubjectKind: "user", SubjectID: user.ID, Role: "member", ScopeKind: "worker", ScopeID: "service-a"}
	if err := svc.DeleteMember(st, target("a"), user.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.Members[domain.MemberKey("a", user.ID)]; ok {
		t.Fatal("membership remained")
	}
	if _, ok := st.Assignments["direct"]; ok {
		t.Fatal("resource role remained")
	}
	if len(st.Groups["group-a"].Members) != 0 || len(st.Groups["group-b"].Members) != 1 {
		t.Fatal("group cleanup crossed organizations")
	}
	for _, org := range []string{"a", "b"} {
		resource, err := authorization.Resolve(st, "worker", "service-"+org, "production")
		if err != nil {
			t.Fatal(err)
		}
		decision, err := authorization.Authorize(st, authorization.Principal{User: user}, "worker.read", resource)
		if err != nil || decision.Allowed != (org == "b") {
			t.Fatalf("%s: %+v %v", org, decision, err)
		}
	}
}

func TestAssignmentsValidateMembershipAndScopeBeforeMutation(t *testing.T) {
	svc, st, user := fixture()
	input := iam.AssignmentInput{SubjectKind: "group", SubjectID: "group-b", Role: "member", ScopeKind: "worker", ScopeID: "service-a"}
	_, err := svc.WriteAssignment(st, target("a"), "", input, false)
	wantCode(t, err, "POLICY_INVALID")
	input.SubjectKind, input.SubjectID, input.ScopeID = "user", user.ID, "service-b"
	_, err = svc.WriteAssignment(st, target("a"), "", input, false)
	wantCode(t, err, "POLICY_INVALID")
	delete(st.Members, domain.MemberKey("a", user.ID))
	input.ScopeID = "service-a"
	_, err = svc.WriteAssignment(st, target("a"), "", input, false)
	wantCode(t, err, "POLICY_INVALID")
	if st.Revision != 0 || len(st.Assignments) != 2 {
		t.Fatal("invalid assignment changed state")
	}
}

func TestKeyPolicyIsACeilingOverCurrentRoles(t *testing.T) {
	svc, st, user := fixture()
	principal := authorization.Principal{User: user, Kind: "session"}
	never := 0
	policy := domain.Policy{Version: 1, Statements: []domain.Statement{{Effect: "allow", Actions: []string{"worker.read"}, Resource: domain.Selector{Kind: "worker", Organization: "a", IDs: []string{"service-b"}}}}}
	_, err := svc.CreateKey(st, principal, target("a"), iam.KeyInput{Name: "deployment", Expires: &never, Policy: policy})
	wantCode(t, err, "POLICY_INVALID")
	if len(st.Keys) != 0 {
		t.Fatal("invalid policy created key")
	}
	policy.Statements[0].Resource.IDs = []string{"service-a"}
	created, err := svc.CreateKey(st, principal, target("a"), iam.KeyInput{Name: "deployment", Expires: &never, Policy: policy})
	if err != nil {
		t.Fatal(err)
	}
	if st.KeyHashes[created.ID] != securetoken.Digest(created.Key) {
		t.Fatal("credential hash mismatch")
	}
	raw, err := json.Marshal(st)
	if err != nil || strings.Contains(string(raw), created.Key) {
		t.Fatal("raw key leaked into working-copy serialization")
	}
	key := st.Keys[created.ID]
	principal.Key = &key
	resource, _ := authorization.Resolve(st, "worker", "service-a", "production")
	decision, err := authorization.Authorize(st, principal, "worker.read", resource)
	if err != nil || !decision.Allowed {
		t.Fatalf("initial grant: %+v %v", decision, err)
	}
	delete(st.Assignments, "grant-a")
	decision, err = authorization.Authorize(st, principal, "worker.read", resource)
	if err != nil || decision.Allowed || !decision.KeyAllowed {
		t.Fatalf("key bypassed revoked role: %+v %v", decision, err)
	}
	if err = svc.RevokeKey(st, authorization.Target{Kind: "api_key", ID: key.ID}); err != nil {
		t.Fatal(err)
	}
	_, err = svc.UpdateKey(st, principal, authorization.Target{Kind: "api_key", ID: key.ID}, iam.KeyUpdateInput{Name: "renamed", Policy: policy, Revision: 2})
	wantCode(t, err, "KEY_INACTIVE")
}

func TestOrganizationCreationAndRevisionConflict(t *testing.T) {
	svc, st, user := fixture()
	created, err := svc.CreateOrganization(st, authorization.Principal{User: user}, iam.OrganizationCreateInput{Name: "new workspace"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := st.Members[domain.MemberKey(created.ID, user.ID)]; !ok || !authorization.IsAdmin(st, user, created.ID) {
		t.Fatal("creator did not receive membership and administrator role")
	}
	_, err = svc.UpdateOrganization(st, target(created.ID), iam.OrganizationInput{Name: "changed", Revision: 0})
	wantCode(t, err, "REVISION_CONFLICT")
	if st.Organizations[created.ID].Name != created.Name {
		t.Fatal("stale update changed organization")
	}
	_, err = svc.UpdateOrganization(st, target(created.ID), iam.OrganizationInput{Name: "changed", Sources: []string{"disabled-source"}, Revision: 1})
	wantCode(t, err, "INVALID_REQUEST")
}
