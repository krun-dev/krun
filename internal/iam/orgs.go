package iam

import (
	"sort"

	"github.com/krun-dev/krun/internal/authorization"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/resourcename"
	"github.com/krun-dev/krun/internal/securetoken"
)

func (s Service) Organizations(st *domain.State, p authorization.Principal) ([]domain.Organization, error) {
	out := []domain.Organization{}
	for _, org := range st.Organizations {
		t, _ := authorization.Resolve(st, "organization", org.ID, "global")
		d, e := authorization.Authorize(st, p, "organization.read", t)
		if e != nil {
			return nil, e
		}
		if d.Allowed {
			out = append(out, org)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s Service) CreateOrganization(st *domain.State, p authorization.Principal, in OrganizationCreateInput) (domain.Organization, error) {
	if !nameValid(in.Name) {
		return domain.Organization{}, domain.Problem(422, "INVALID_REQUEST", "Name required (max 128 bytes)")
	}
	shortName, err := resourcename.Make(firstName(in.ShortName, in.Name), 1, resourcename.OrganizationMax)
	if err != nil {
		return domain.Organization{}, err
	}
	for _, existing := range st.Organizations {
		if existing.ShortName == shortName {
			return domain.Organization{}, domain.Problem(409, "LOCATOR_CONFLICT", "Organization short name already exists")
		}
	}
	org := domain.Organization{ShortName: shortName, ID: securetoken.NewID("org"), Name: in.Name, Sources: []string{}, Revision: 1}
	st.Organizations[org.ID] = org
	st.Members[domain.MemberKey(org.ID, p.User.ID)] = domain.Member{Organization: org.ID, User: p.User.ID}
	id := securetoken.NewID("role")
	st.Assignments[id] = domain.Assignment{ID: id, SubjectKind: "user", SubjectID: p.User.ID, Role: "org_admin", ScopeKind: "organization", ScopeID: org.ID, Revision: 1}
	st.Revision++
	return org, nil
}

func (s Service) UpdateOrganization(st *domain.State, t authorization.Target, in OrganizationInput) (domain.Organization, error) {
	if !nameValid(in.Name) {
		return domain.Organization{}, domain.Problem(422, "INVALID_REQUEST", "Name required")
	}
	for _, src := range in.Sources {
		if !s.sourceEnabled(src) {
			return domain.Organization{}, domain.Problem(422, "INVALID_REQUEST", "Unknown or disabled source")
		}
	}
	org := st.Organizations[t.ID]
	if in.Revision != org.Revision {
		return domain.Organization{}, domain.Problem(409, "REVISION_CONFLICT", "Organization changed")
	}
	org.Name = in.Name
	org.Sources = in.Sources
	org.Revision++
	st.Organizations[t.ID] = org
	st.Revision++
	return org, nil
}

func (s Service) hasOrganizationAdmin(st *domain.State, org string) bool {
	for _, assignment := range st.Assignments {
		if s.isActiveOrganizationAdminGrant(st, org, assignment) {
			return true
		}
	}
	return false
}

func (s Service) isActiveOrganizationAdminGrant(st *domain.State, org string, assignment domain.Assignment) bool {
	if assignment.Role != "org_admin" || assignment.ScopeKind != "organization" || assignment.ScopeID != org {
		return false
	}

	isEligible := func(id string) bool {
		user := st.Users[id]
		_, member := st.Members[domain.MemberKey(org, id)]
		return member && user.Status == "active" &&
			s.sourceEnabled(user.Source) && authorization.SourceAllowed(st, user, org)
	}

	if assignment.SubjectKind == "user" {
		return isEligible(assignment.SubjectID)
	}
	group, ok := st.Groups[assignment.SubjectID]
	if !ok || group.ScopeKind != "organization" || group.ScopeID != org {
		return false
	}
	for _, member := range group.Members {
		if isEligible(member) {
			return true
		}
	}
	return false
}

func (s Service) finishOrganizationAdminChange(st *domain.State, org string, hadAdmin bool) error {
	if hadAdmin && !s.hasOrganizationAdmin(st, org) {
		return domain.Problem(409, "LAST_ORG_ADMIN", "Grant another active member the organization administrator role first.")
	}

	// The organization revision is the shared CAS for changes to its admin
	// grants and memberships, even when the writes affect different rows.
	organization := st.Organizations[org]
	organization.Revision++
	st.Organizations[org] = organization
	return nil
}

// MembershipRoles reports the current user's own effective assignment names;
// it does not translate them into Cloudflare roles or grant new permissions.
func (s Service) MembershipRoles(st *domain.State, p authorization.Principal, org string) []string {
	names := map[string]bool{}
	for _, a := range st.Assignments {
		if !authorization.RoleMatches(st, a, p.User) {
			continue
		}
		if a.ScopeKind == "system" && a.Role == "system_admin" || a.ScopeKind == "organization" && a.ScopeID == org {
			names[a.Role] = true
		}
	}
	out := []string{}
	for name := range names {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
