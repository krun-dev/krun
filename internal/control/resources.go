package control

import (
	"net/http"

	"github.com/krun-dev/krun/internal/iam"
)

func resourceKind(r *http.Request) string {
	if containsPath(r.Pattern, "models") {
		return "model"
	}
	return "service"
}

func containsPath(path, part string) bool {
	for i := 0; i+len(part) <= len(path); i++ {
		if path[i:i+len(part)] == part {
			return true
		}
	}
	return false
}

func (s *Server) resourceTarget(st *State, r *http.Request) (Target, error) {
	t, e := resolve(st, resourceKind(r), r.PathValue("resource"), "global")
	if e != nil || t.Organization != r.PathValue("org") {
		return t, notFound()
	}
	return t, nil
}

func (s *Server) resourceList(st *State, p Principal, r *http.Request) (any, int, error) {
	t, e := s.orgTarget(st, r)
	if e != nil {
		return nil, 0, e
	}
	kind := resourceKind(r)
	if e = s.require(st, p, r, kind+".list", t); e != nil {
		return nil, 0, e
	}
	out, err := s.iam().Resources(st, p, t, kind)
	if err != nil {
		return nil, 0, err
	}
	return resourcePageItems(st, r, out)
}

func (s *Server) resourceCreate(st *State, p Principal, r *http.Request) (any, int, error) {
	t, e := s.orgTarget(st, r)
	if e != nil {
		return nil, 0, e
	}
	kind := resourceKind(r)
	if e = s.require(st, p, r, kind+".create", t); e != nil {
		return nil, 0, e
	}
	var in iam.NameInput
	if e = body(r, &in); e != nil {
		return nil, 0, e
	}
	out, err := s.iam().CreateResource(st, t, kind, in)
	return out, 201, err
}

func (s *Server) resourceDetail(st *State, p Principal, r *http.Request) (any, int, error) {
	t, e := s.resourceTarget(st, r)
	if e != nil {
		return nil, 0, e
	}
	if e = s.require(st, p, r, t.Kind+".read", t); e != nil {
		if denied, ok := e.(*APIError); ok && denied.Status == 403 {
			return nil, 0, notFound()
		}
		return nil, 0, e
	}
	return st.Resources[t.ID], 200, nil
}

func (s *Server) resourceUpdate(st *State, p Principal, r *http.Request) (any, int, error) {
	t, e := s.resourceTarget(st, r)
	if e != nil {
		return nil, 0, e
	}
	if e = s.require(st, p, r, t.Kind+".update", t); e != nil {
		return nil, 0, e
	}
	var in iam.RevisionNameInput
	if e = body(r, &in); e != nil {
		return nil, 0, e
	}
	out, err := s.iam().UpdateResource(st, t, in)
	return out, 200, err
}

func (s *Server) resourceDelete(st *State, p Principal, r *http.Request) (any, int, error) {
	t, e := s.resourceTarget(st, r)
	if e != nil {
		return nil, 0, e
	}
	if e = s.require(st, p, r, t.Kind+".delete", t); e != nil {
		return nil, 0, e
	}
	err := s.iam().DeleteResource(st, t)
	return nil, 204, err
}
