package control

import (
	"net/http"

	"github.com/krun-dev/krun/internal/iam"
)

func (s *Server) assignmentList(st *State, p Principal, r *http.Request) (any, int, error) {
	t, e := s.scopeRequire(st, p, r, "read")
	if e != nil {
		return nil, 0, e
	}
	out, err := s.iam().Assignments(st, t)
	if err != nil {
		return nil, 0, err
	}
	return items(out)
}

func (s *Server) assignmentCreate(st *State, p Principal, r *http.Request) (any, int, error) {
	return s.assignmentWrite(st, p, r, false)
}

func (s *Server) assignmentUpdate(st *State, p Principal, r *http.Request) (any, int, error) {
	return s.assignmentWrite(st, p, r, true)
}

func (s *Server) assignmentWrite(st *State, p Principal, r *http.Request, update bool) (any, int, error) {
	t, e := s.scopeRequire(st, p, r, "grant")
	if e != nil {
		return nil, 0, e
	}
	var in iam.AssignmentInput
	if e = body(r, &in); e != nil {
		return nil, 0, e
	}
	out, err := s.iam().WriteAssignment(st, t, r.PathValue("assignment"), in, update)
	return out, 201, err
}

func (s *Server) assignmentDelete(st *State, p Principal, r *http.Request) (any, int, error) {
	t, e := s.scopeRequire(st, p, r, "revoke")
	if e != nil {
		return nil, 0, e
	}
	err := s.iam().DeleteAssignment(st, t, r.PathValue("assignment"))
	return nil, 204, err
}
