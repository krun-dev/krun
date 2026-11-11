package iam

import (
	"github.com/krun-dev/krun/internal/authorization"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/securetoken"
)

func remove(values []string, item string) []string {
	out := []string{}
	for _, v := range values {
		if v != item {
			out = append(out, v)
		}
	}
	return out
}
func sameScope(g domain.Group, t authorization.Target) bool {
	return g.ScopeKind == t.Kind && g.ScopeID == t.ID
}

func (s Service) Groups(st *domain.State, t authorization.Target) ([]domain.Group, error) {
	out := []domain.Group{}
	for _, g := range st.Groups {
		if sameScope(g, t) {
			out = append(out, g)
		}
	}
	return out, nil
}

func (s Service) CreateGroup(st *domain.State, t authorization.Target, in NameInput) (domain.Group, error) {
	if !nameValid(in.Name) {
		return domain.Group{}, domain.Invalid("INVALID_REQUEST", "Name required")
	}
	g := domain.Group{ID: securetoken.NewID("grp"), ScopeKind: t.Kind, ScopeID: t.ID, Name: in.Name, Members: []string{}, Revision: 1}
	st.Groups[g.ID] = g
	st.Revision++
	return g, nil
}

func (s Service) UpdateGroup(st *domain.State, t authorization.Target, id string, in RevisionNameInput) (domain.Group, error) {
	g, err := s.Group(st, t, id)
	if err != nil {
		return domain.Group{}, err
	}
	if in.Revision != g.Revision {
		return domain.Group{}, domain.Conflict("REVISION_CONFLICT", "Group changed")
	}
	if !nameValid(in.Name) {
		return domain.Group{}, domain.Invalid("INVALID_REQUEST", "Name required")
	}
	g.Name = in.Name
	g.Revision++
	st.Groups[g.ID] = g
	st.Revision++
	return g, nil
}

func (s Service) DeleteGroup(st *domain.State, t authorization.Target, id string) error {
	g, err := s.Group(st, t, id)
	if err != nil {
		return err
	}
	hadAdmin := t.Kind == "organization" && s.hasOrganizationAdmin(st, t.ID)
	delete(st.Groups, g.ID)
	for id, a := range st.Assignments {
		if a.SubjectKind == "group" && a.SubjectID == g.ID {
			delete(st.Assignments, id)
		}
	}
	st.Revision++
	if t.Kind == "organization" {
		if err := s.finishOrganizationAdminChange(st, t.ID, hadAdmin); err != nil {
			return err
		}
	}
	if e := s.ensureAdmin(st); e != nil {
		return e
	}
	return nil
}

func (s Service) SetGroupMember(st *domain.State, t authorization.Target, id, user string, add bool) (domain.Group, error) {
	g, err := s.Group(st, t, id)
	if err != nil {
		return domain.Group{}, err
	}
	u, ok := st.Users[user]
	if !ok || (add && u.Status != "active") {
		return domain.Group{}, domain.Invalid("INVALID_REQUEST", "Active user required")
	}
	if t.Kind == "organization" {
		if _, ok := st.Members[domain.MemberKey(t.ID, user)]; !ok {
			return domain.Group{}, domain.Invalid("INVALID_REQUEST", "Organization membership required")
		}
	}
	changesAdmin := t.Kind == "organization" && !add
	hadAdmin := changesAdmin && s.hasOrganizationAdmin(st, t.ID)
	g.Members = remove(g.Members, user)
	if add {
		g.Members = append(g.Members, user)
	}
	g.Revision++
	st.Groups[g.ID] = g
	st.Revision++
	if changesAdmin {
		if err := s.finishOrganizationAdminChange(st, t.ID, hadAdmin); err != nil {
			return domain.Group{}, err
		}
	}
	if e := s.ensureAdmin(st); e != nil {
		return domain.Group{}, e
	}
	return g, nil
}
