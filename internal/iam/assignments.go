package iam

import (
	"github.com/krun-dev/krun/internal/authorization"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/securetoken"
)

func validateAssignment(st *domain.State, t authorization.Target, in AssignmentInput) error {
	if t.Kind == "system" {
		if in.Role != "system_admin" || in.ScopeKind != "system" || in.ScopeID != "system" || len(in.Conditions.Environments) > 0 {
			return domain.Invalid("POLICY_INVALID", "System scope permits only system_admin")
		}
	} else {
		if !authorization.OrganizationRoleValid(in.Role) {
			return domain.Invalid("POLICY_INVALID", "Unknown organization role")
		}
		if in.ScopeKind == "organization" {
			if in.ScopeID != t.ID {
				return domain.Invalid("POLICY_INVALID", "Wrong organization scope")
			}
		} else {
			res, ok := st.Resources[in.ScopeID]
			if !ok || res.Organization != t.ID || res.Kind != in.ScopeKind || in.Role == "org_admin" {
				return domain.Invalid("POLICY_INVALID", "Invalid resource scope")
			}
		}
		if in.Role == "org_admin" && len(in.Conditions.Environments) > 0 {
			return domain.Invalid("POLICY_INVALID", "Administrator roles cannot have environment conditions")
		}
	}
	for _, env := range in.Conditions.Environments {
		if !authorization.ReservedEnvironmentValid(env) {
			return domain.Invalid("POLICY_INVALID", "Unknown environment")
		}
	}
	if in.SubjectKind == "user" {
		u, ok := st.Users[in.SubjectID]
		if !ok || u.Status != "active" {
			return domain.Invalid("POLICY_INVALID", "Active user required")
		}
		if t.Kind == "organization" {
			if _, ok := st.Members[domain.MemberKey(t.ID, u.ID)]; !ok {
				return domain.Invalid("POLICY_INVALID", "Organization membership required")
			}
		}
	} else if in.SubjectKind == "group" {
		g, ok := st.Groups[in.SubjectID]
		if !ok || !sameScope(g, t) {
			return domain.Invalid("POLICY_INVALID", "Group must belong to this scope")
		}
	} else {
		return domain.Invalid("POLICY_INVALID", "Subject must be user or group")
	}
	return nil
}
func assignmentScope(st *domain.State, a domain.Assignment, t authorization.Target) bool {
	if t.Kind == "system" {
		return a.ScopeKind == "system"
	}
	return a.ScopeKind != "system" && authorization.ScopeOrganization(st, a) == t.ID
}

type ListedAssignment struct {
	domain.Assignment
	RevokeBlocked bool `json:"revoke_blocked,omitempty"`
}

func (s Service) Assignments(st *domain.State, t authorization.Target) ([]ListedAssignment, error) {
	out := []ListedAssignment{}
	activeAdminGrants := 0
	lastAdminIndex := 0

	for _, assignment := range st.Assignments {
		if !assignmentScope(st, assignment, t) {
			continue
		}
		if t.Kind == "organization" && s.isActiveOrganizationAdminGrant(st, t.ID, assignment) {
			activeAdminGrants++
			lastAdminIndex = len(out)
		}
		out = append(out, ListedAssignment{Assignment: assignment})
	}
	if activeAdminGrants == 1 {
		out[lastAdminIndex].RevokeBlocked = true
	}
	return out, nil
}

func (s Service) WriteAssignment(st *domain.State, t authorization.Target, assignmentID string, in AssignmentInput, update bool) (domain.Assignment, error) {
	if target, err := authorization.ResolveReference(st, in.ScopeKind, in.ScopeID, "global"); err == nil {
		in.ScopeID = target.ID
	}
	if e := validateAssignment(st, t, in); e != nil {
		return domain.Assignment{}, e
	}
	id := securetoken.NewID("role")
	rev := 1
	changesAdmin := t.Kind == "organization" && in.Role == "org_admin"
	if update {
		id = assignmentID
		old, ok := st.Assignments[id]
		if !ok || !assignmentScope(st, old, t) {
			return domain.Assignment{}, domain.NotFound()
		}
		if in.Revision != old.Revision {
			return domain.Assignment{}, domain.Conflict("REVISION_CONFLICT", "Assignment changed")
		}
		rev = old.Revision + 1
		changesAdmin = changesAdmin || (t.Kind == "organization" && old.Role == "org_admin")
	}
	hadAdmin := changesAdmin && s.hasOrganizationAdmin(st, t.ID)
	a := domain.Assignment{ID: id, SubjectKind: in.SubjectKind, SubjectID: in.SubjectID, Role: in.Role, ScopeKind: in.ScopeKind, ScopeID: in.ScopeID, Conditions: in.Conditions, Revision: rev}
	st.Assignments[id] = a
	st.Revision++
	if changesAdmin {
		if err := s.finishOrganizationAdminChange(st, t.ID, hadAdmin); err != nil {
			return domain.Assignment{}, err
		}
	}
	if e := s.ensureAdmin(st); e != nil {
		return domain.Assignment{}, e
	}
	return a, nil
}

func (s Service) DeleteAssignment(st *domain.State, t authorization.Target, id string) error {
	a, ok := st.Assignments[id]
	if !ok || !assignmentScope(st, a, t) {
		return domain.NotFound()
	}
	changesAdmin := t.Kind == "organization" && a.Role == "org_admin"
	hadAdmin := changesAdmin && s.hasOrganizationAdmin(st, t.ID)
	delete(st.Assignments, a.ID)
	st.Revision++
	if changesAdmin {
		if err := s.finishOrganizationAdminChange(st, t.ID, hadAdmin); err != nil {
			return err
		}
	}
	if e := s.ensureAdmin(st); e != nil {
		return e
	}
	return nil
}
