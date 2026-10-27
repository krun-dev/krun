package control

import (
	"net/http"
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
	out := []Resource{}
	for _, res := range st.Resources {
		if res.Organization != t.ID || res.Kind != kind {
			continue
		}
		target, _ := resolve(st, kind, res.ID, "global")
		d, e := Authorize(st, p, kind+".read", target)
		if e != nil {
			return nil, 0, e
		}
		if d.Allowed {
			out = append(out, res)
		}
	}
	return items(out)
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
	var in struct {
		Name string `json:"name"`
	}
	if e = body(r, &in); e != nil {
		return nil, 0, e
	}
	if !nameValid(in.Name) {
		return nil, 0, problem(422, "INVALID_REQUEST", "Name required")
	}
	res := Resource{ID: randomID(kind), Kind: kind, Name: in.Name, Organization: t.ID, Revision: 1}
	st.Resources[res.ID] = res
	st.Revision++
	return res, 201, nil
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
	var in struct {
		Name     string `json:"name"`
		Revision int    `json:"expected_revision"`
	}
	if e = body(r, &in); e != nil {
		return nil, 0, e
	}
	res := st.Resources[t.ID]
	if in.Revision != res.Revision {
		return nil, 0, problem(409, "REVISION_CONFLICT", "Resource changed")
	}
	if !nameValid(in.Name) {
		return nil, 0, problem(422, "INVALID_REQUEST", "Name required")
	}
	res.Name = in.Name
	res.Revision++
	st.Resources[t.ID] = res
	st.Revision++
	return res, 200, nil
}
func (s *Server) resourceDelete(st *State, p Principal, r *http.Request) (any, int, error) {
	t, e := s.resourceTarget(st, r)
	if e != nil {
		return nil, 0, e
	}
	if e = s.require(st, p, r, t.Kind+".delete", t); e != nil {
		return nil, 0, e
	}
	delete(st.Resources, t.ID)
	deletedGroups := map[string]bool{}
	for id, group := range st.Groups {
		if group.ScopeKind == t.Kind && group.ScopeID == t.ID {
			deletedGroups[id] = true
			delete(st.Groups, id)
		}
	}
	for id, a := range st.Assignments {
		if (a.ScopeKind == t.Kind && a.ScopeID == t.ID) || (a.SubjectKind == "group" && deletedGroups[a.SubjectID]) {
			delete(st.Assignments, id)
		}
	}
	st.Revision++
	return nil, 204, nil
}
