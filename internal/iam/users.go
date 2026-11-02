package iam

import (
	"sort"

	"github.com/krun-dev/krun/internal/authorization"
	"github.com/krun-dev/krun/internal/domain"
)

func (s Service) ensureAdmin(st *domain.State) error {
	if !st.Bootstrapped {
		return nil
	}
	for _, u := range st.Users {
		if u.Status == "active" && s.sourceEnabled(u.Source) && authorization.IsAdmin(st, u, "") {
			return nil
		}
	}
	return domain.Problem(409, "LAST_ADMIN", "At least one active system administrator must remain")
}

func (s Service) Users(st *domain.State) ([]domain.User, error) {
	out := []domain.User{}
	for _, u := range st.Users {
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s Service) UpdateUser(st *domain.State, id string, in UserInput) (domain.User, error) {
	u, err := s.User(st, id)
	if err != nil {
		return domain.User{}, err
	}
	if in.Status != "active" && in.Status != "disabled" {
		return domain.User{}, domain.Problem(422, "INVALID_REQUEST", "Status must be active or disabled")
	}
	u.Status = in.Status
	u.AuthVersion++
	st.Users[u.ID] = u
	st.Revision++
	if e := s.ensureAdmin(st); e != nil {
		return domain.User{}, e
	}
	return u, nil
}

func (s Service) Invite(st *domain.State, in InvitationInput) error {
	if !s.sourceEnabled(in.Source) || in.Subject == "" || len(in.Subject) > 512 {
		return domain.Problem(422, "INVALID_REQUEST", "Enabled source and stable subject required")
	}
	return AddInvitation(st, in.Source, in.Subject)
}
