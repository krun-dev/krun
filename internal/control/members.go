package control

import (
	"net/http"

	"github.com/krun-dev/krun/internal/iam"
)

func (s *Server) members(st *State, p Principal, r *http.Request) (any, int, error) {
	t, e := s.orgTarget(st, r)
	if e != nil {
		return nil, 0, e
	}
	if e = s.require(st, p, r, "member.read", t); e != nil {
		return nil, 0, e
	}
	out, err := s.iam().Members(st, t)
	if err != nil {
		return nil, 0, err
	}
	return items(out)
}

func (s *Server) memberAdd(st *State, p Principal, r *http.Request) (any, int, error) {
	t, e := s.orgTarget(st, r)
	if e != nil {
		return nil, 0, e
	}
	if e = s.require(st, p, r, "member.manage", t); e != nil {
		return nil, 0, e
	}
	var in iam.MemberInput
	if e = body(r, &in); e != nil {
		return nil, 0, e
	}
	out, err := s.iam().AddMember(st, t, in)
	return out, 201, err
}

func (s *Server) memberDelete(st *State, p Principal, r *http.Request) (any, int, error) {
	t, e := s.orgTarget(st, r)
	if e != nil {
		return nil, 0, e
	}
	if e = s.require(st, p, r, "member.manage", t); e != nil {
		return nil, 0, e
	}
	err := s.iam().DeleteMember(st, t, r.PathValue("user"))
	return nil, 204, err
}
