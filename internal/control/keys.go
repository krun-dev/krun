package control

import (
	"net/http"

	"github.com/krun-dev/krun/internal/iam"
)

func (s *Server) keyCreate(st *State, p Principal, r *http.Request) (any, int, error) {
	t, e := s.scopeTarget(st, r, "grant")
	if e != nil {
		return nil, 0, e
	}
	if e = s.require(st, p, r, "apiKey.create", t); e != nil {
		return nil, 0, e
	}
	var in iam.KeyInput
	if e = body(r, &in); e != nil {
		return nil, 0, e
	}
	out, err := s.iam().CreateKey(st, p, t, in)
	return out, 201, err
}

func (s *Server) keyList(st *State, p Principal, r *http.Request) (any, int, error) {
	out, err := s.iam().Keys(st, p, r.PathValue("org"))
	if err != nil {
		return nil, 0, err
	}
	return items(out)
}

func (s *Server) keyDetail(st *State, p Principal, r *http.Request) (any, int, error) {
	t, e := resolve(st, "api_key", r.PathValue("key"), "global")
	if e != nil {
		return nil, 0, e
	}
	if e = s.require(st, p, r, "apiKey.read", t); e != nil {
		if denied, ok := e.(*APIError); ok && denied.HTTPStatus() == 403 {
			return nil, 0, notFound()
		}
		return nil, 0, e
	}
	return st.Keys[t.ID], 200, nil
}

func (s *Server) keyUpdate(st *State, p Principal, r *http.Request) (any, int, error) {
	t, e := resolve(st, "api_key", r.PathValue("key"), "global")
	if e != nil {
		return nil, 0, e
	}
	if e = s.require(st, p, r, "apiKey.update", t); e != nil {
		return nil, 0, e
	}
	if _, err := s.iam().ActiveKey(st, t.ID); err != nil {
		return nil, 0, err
	}
	var in iam.KeyUpdateInput
	if e = body(r, &in); e != nil {
		return nil, 0, e
	}
	out, err := s.iam().UpdateKey(st, p, t, in)
	return out, 200, err
}

func (s *Server) keyDelete(st *State, p Principal, r *http.Request) (any, int, error) {
	t, e := resolve(st, "api_key", r.PathValue("key"), "global")
	if e != nil {
		return nil, 0, e
	}
	if e = s.require(st, p, r, "apiKey.revoke", t); e != nil {
		return nil, 0, e
	}
	err := s.iam().RevokeKey(st, t)
	return nil, 204, err
}
