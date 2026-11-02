package control

import (
	"net/http"

	"github.com/krun-dev/krun/internal/iam"
)

func (s *Server) users(st *State, p Principal, r *http.Request) (any, int, error) {
	if e := s.require(st, p, r, "system.user.read", systemTarget()); e != nil {
		return nil, 0, e
	}
	out, err := s.iam().Users(st)
	if err != nil {
		return nil, 0, err
	}
	return items(out)
}

func (s *Server) userUpdate(st *State, p Principal, r *http.Request) (any, int, error) {
	if e := s.require(st, p, r, "system.user.update", systemTarget()); e != nil {
		return nil, 0, e
	}
	if _, err := s.iam().User(st, r.PathValue("user")); err != nil {
		return nil, 0, err
	}
	var in iam.UserInput
	if e := body(r, &in); e != nil {
		return nil, 0, e
	}
	out, err := s.iam().UpdateUser(st, r.PathValue("user"), in)
	return out, 200, err
}

func (s *Server) inviteHandler(st *State, p Principal, r *http.Request) (any, int, error) {
	if e := s.require(st, p, r, "system.user.update", systemTarget()); e != nil {
		return nil, 0, e
	}
	var in iam.InvitationInput
	if e := body(r, &in); e != nil {
		return nil, 0, e
	}
	err := s.iam().Invite(st, in)
	return map[string]string{"status": "invited"}, 201, err
}
