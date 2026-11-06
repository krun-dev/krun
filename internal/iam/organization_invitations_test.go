package iam_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/krun-dev/krun/internal/authorization"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/iam"
)

func invitationFixture(t *testing.T) (iam.Service, *domain.State, domain.OrganizationInvitation, authorization.Principal) {
	t.Helper()
	svc := iam.NewService(func(source string) bool { return source == "local" || source == "other" })
	st := domain.NewState()
	st.Organizations["org"] = domain.Organization{ID: "org", Name: "Example team", Revision: 1}
	creator := domain.User{ID: "creator", Source: "local", Status: "active", AuthVersion: 1}
	user := domain.User{ID: "recipient", Source: "local", Status: "active", Email: "person@example.com", AuthVersion: 1}
	st.Users[creator.ID] = creator
	st.Users[user.ID] = user
	st.Members[domain.MemberKey("org", creator.ID)] = domain.Member{Organization: "org", User: creator.ID}
	st.Assignments["admin"] = domain.Assignment{
		ID: "admin", SubjectKind: "user", SubjectID: creator.ID,
		Role: "org_admin", ScopeKind: "organization", ScopeID: "org", Revision: 1,
	}
	in, _, err := svc.NewOrganizationInvitation(st, "org", creator.ID, iam.OrganizationInvitationInput{}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	return svc, st, in, authorization.Principal{User: user, Kind: "session"}
}

func TestOrganizationInvitationDefaultsAndValidation(t *testing.T) {
	svc, st, in, _ := invitationFixture(t)
	if in.Role != "member" || in.MaxUses != 1 || in.Expires.Sub(in.Created) != 7*24*time.Hour {
		t.Fatalf("unexpected invitation defaults: %+v", in)
	}
	for _, input := range []iam.OrganizationInvitationInput{
		{Role: "org_admin"}, {Role: "system_admin"}, {Role: "developer"},
		{MaxUses: intPointer(0)}, {MaxUses: intPointer(101)},
		{TargetUser: "missing"}, {TargetEmail: "not-an-email"},
		{TargetEmail: "Name <person@example.com>"},
		{TargetUser: "recipient", TargetEmail: "person@example.com"},
	} {
		_, _, err := svc.NewOrganizationInvitation(st, "org", "creator", input, time.Now().UTC())
		wantCode(t, err, "INVALID_REQUEST")
	}
	in, token, err := svc.NewOrganizationInvitation(st, "org", "creator", iam.OrganizationInvitationInput{Role: "viewer", MaxUses: intPointer(100), TargetEmail: " Person@Example.com "}, time.Now().UTC())
	if err != nil || in.TargetEmail != "person@example.com" || in.MaxUses != 100 || !strings.HasPrefix(token, "krun_invite_") {
		t.Fatalf("valid directed invitation: %+v %v", in, err)
	}
	raw, err := json.Marshal(in)
	if err != nil || strings.Contains(string(raw), token) || strings.Contains(string(raw), in.TokenHash) {
		t.Fatal("invitation metadata leaked a credential")
	}
}

func intPointer(value int) *int { return &value }

func TestOrganizationInvitationRecipientAndExistingMembership(t *testing.T) {
	svc, st, in, p := invitationFixture(t)
	in.TargetEmail = "person@example.com"
	_, _, err := svc.AcceptOrganizationInvitation(st, in, p, false, time.Now().UTC())
	wantCode(t, err, "INVITATION_RECIPIENT_MISMATCH")
	p.User.EmailVerified = true
	p.User.Email = "PERSON@EXAMPLE.COM"
	result, assignment, err := svc.AcceptOrganizationInvitation(st, in, p, false, time.Now().UTC())
	if err != nil || result.Status != "joined" || assignment.Role != "member" || assignment.ScopeID != "org" {
		t.Fatalf("verified recipient: %+v %+v %v", result, assignment, err)
	}
	in.TargetEmail = ""
	in.TargetUser = "another-user"
	_, _, err = svc.AcceptOrganizationInvitation(st, in, p, false, time.Now().UTC())
	wantCode(t, err, "INVITATION_RECIPIENT_MISMATCH")
	in.TargetUser = p.User.ID
	_, _, err = svc.AcceptOrganizationInvitation(st, in, p, true, time.Now().UTC())
	wantCode(t, err, "INVITATION_ALREADY_USED")
	st.Members[domain.MemberKey("org", p.User.ID)] = domain.Member{Organization: "org", User: p.User.ID}
	in.UseCount = in.MaxUses
	before, _ := json.Marshal(st)
	result, assignment, err = svc.AcceptOrganizationInvitation(st, in, p, true, time.Now().UTC())
	if err != nil || result.Status != "already_member" || result.CanEnter || assignment.ID != "" {
		t.Fatalf("existing unassigned member: %+v %+v %v", result, assignment, err)
	}
	after, _ := json.Marshal(st)
	if string(before) != string(after) {
		t.Fatal("existing membership changed the working copy")
	}
}

func TestOrganizationInvitationAvailabilityAndSourceRestrictions(t *testing.T) {
	for _, condition := range []string{"expired", "revoked", "creator-disabled", "creator-revoked", "full"} {
		t.Run(condition, func(t *testing.T) {
			svc, st, in, _ := invitationFixture(t)
			switch condition {
			case "expired":
				in.Expires = time.Now().Add(-time.Second)
			case "revoked":
				at := time.Now()
				in.Revoked = &at
			case "creator-disabled":
				user := st.Users[in.Creator]
				user.Status = "disabled"
				st.Users[user.ID] = user
			case "creator-revoked":
				delete(st.Assignments, "admin")
			case "full":
				in.UseCount = in.MaxUses
			}
			_, err := svc.PreviewOrganizationInvitation(st, in, time.Now().UTC())
			wantCode(t, err, "INVITATION_UNAVAILABLE")
		})
	}
	svc, st, in, p := invitationFixture(t)
	org := st.Organizations["org"]
	org.Sources = []string{"local"}
	st.Organizations[org.ID] = org
	p.User.Source = "other"
	_, _, err := svc.AcceptOrganizationInvitation(st, in, p, false, time.Now().UTC())
	wantCode(t, err, "INVITATION_SOURCE_DENIED")
	for _, kind := range []string{"api_key", "cli_session"} {
		p.Kind = kind
		_, _, err := svc.AcceptOrganizationInvitation(st, in, p, false, time.Now().UTC())
		wantCode(t, err, "PERMISSION_DENIED")
	}
}
