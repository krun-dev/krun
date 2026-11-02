package iam

import (
	"github.com/krun-dev/krun/internal/authorization"
	"github.com/krun-dev/krun/internal/domain"
)

func (s Service) Members(st *domain.State, t authorization.Target) ([]domain.User, error) {
	out := []domain.User{}
	for _, m := range st.Members {
		if m.Organization == t.ID {
			out = append(out, st.Users[m.User])
		}
	}
	return out, nil
}

func (s Service) AddMember(st *domain.State, t authorization.Target, in MemberInput) (domain.Member, error) {
	u, ok := st.Users[in.User]
	if !ok || u.Status != "active" {
		return domain.Member{}, domain.Problem(422, "INVALID_REQUEST", "Existing active local user_id required")
	}
	m := domain.Member{Organization: t.ID, User: u.ID}
	st.Members[domain.MemberKey(t.ID, u.ID)] = m
	st.Revision++
	return m, nil
}

func (s Service) DeleteMember(st *domain.State, t authorization.Target, user string) error {
	if _, ok := st.Members[domain.MemberKey(t.ID, user)]; !ok {
		return domain.NotFound()
	}
	delete(st.Members, domain.MemberKey(t.ID, user))
	for id, g := range st.Groups {
		if g.ScopeKind == "organization" && g.ScopeID == t.ID {
			g.Members = remove(g.Members, user)
			g.Revision++
			st.Groups[id] = g
		}
	}
	for id, a := range st.Assignments {
		if a.SubjectKind == "user" && a.SubjectID == user && authorization.ScopeOrganization(st, a) == t.ID {
			delete(st.Assignments, id)
		}
	}
	st.Revision++
	return nil
}
