package control

import (
	"net/http"

	"github.com/krun-dev/krun/internal/iam"
)

func (s *Server) groupList(st *State, p Principal, r *http.Request) (any, int, error) {
	t, e := s.scopeRequire(st, p, r, "read")
	if e != nil {
		return nil, 0, e
	}
	out, err := s.iam().Groups(st, t)
	if err != nil {
		return nil, 0, err
	}
	return items(out)
}

func (s *Server) groupCreate(st *State, p Principal, r *http.Request) (any, int, error) {
	t, e := s.scopeRequire(st, p, r, "grant")
	if e != nil {
		return nil, 0, e
	}
	var in iam.NameInput
	if e = body(r, &in); e != nil {
		return nil, 0, e
	}
	out, err := s.iam().CreateGroup(st, t, in)
	return out, 201, err
}

func (s *Server) groupUpdate(st *State, p Principal, r *http.Request) (any, int, error) {
	t, e := s.scopeRequire(st, p, r, "grant")
	if e != nil {
		return nil, 0, e
	}
	if _, e = s.iam().Group(st, t, r.PathValue("group")); e != nil {
		return nil, 0, e
	}
	var in iam.RevisionNameInput
	if e = body(r, &in); e != nil {
		return nil, 0, e
	}
	out, err := s.iam().UpdateGroup(st, t, r.PathValue("group"), in)
	return out, 200, err
}

func (s *Server) groupDelete(st *State, p Principal, r *http.Request) (any, int, error) {
	t, e := s.scopeRequire(st, p, r, "revoke")
	if e != nil {
		return nil, 0, e
	}
	err := s.iam().DeleteGroup(st, t, r.PathValue("group"))
	return nil, 204, err
}

func (s *Server) groupMember(st *State, p Principal, r *http.Request) (any, int, error) {
	verb := "grant"
	if r.Method == "DELETE" {
		verb = "revoke"
	}
	t, e := s.scopeRequire(st, p, r, verb)
	if e != nil {
		return nil, 0, e
	}
	out, err := s.iam().SetGroupMember(st, t, r.PathValue("group"), r.PathValue("user"), r.Method == http.MethodPut)
	return out, 200, err
}
