package control

import (
	"net/http"
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
func sameScope(g Group, t Target) bool { return g.ScopeKind == t.Kind && g.ScopeID == t.ID }
func (s *Server) groupList(st *State, p Principal, r *http.Request) (any, int, error) {
	t, e := s.scopeRequire(st, p, r, "read")
	if e != nil {
		return nil, 0, e
	}
	out := []Group{}
	for _, g := range st.Groups {
		if sameScope(g, t) {
			out = append(out, g)
		}
	}
	return items(out)
}
func (s *Server) groupCreate(st *State, p Principal, r *http.Request) (any, int, error) {
	t, e := s.scopeRequire(st, p, r, "grant")
	if e != nil {
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
	g := Group{ID: randomID("grp"), ScopeKind: t.Kind, ScopeID: t.ID, Name: in.Name, Members: []string{}, Revision: 1}
	st.Groups[g.ID] = g
	st.Revision++
	return g, 201, nil
}
func (s *Server) groupUpdate(st *State, p Principal, r *http.Request) (any, int, error) {
	t, e := s.scopeRequire(st, p, r, "grant")
	if e != nil {
		return nil, 0, e
	}
	g, ok := st.Groups[r.PathValue("group")]
	if !ok || !sameScope(g, t) {
		return nil, 0, notFound()
	}
	var in struct {
		Name     string `json:"name"`
		Revision int    `json:"expected_revision"`
	}
	if e = body(r, &in); e != nil {
		return nil, 0, e
	}
	if in.Revision != g.Revision {
		return nil, 0, problem(409, "REVISION_CONFLICT", "Group changed")
	}
	if !nameValid(in.Name) {
		return nil, 0, problem(422, "INVALID_REQUEST", "Name required")
	}
	g.Name = in.Name
	g.Revision++
	st.Groups[g.ID] = g
	st.Revision++
	return g, 200, nil
}
func (s *Server) groupDelete(st *State, p Principal, r *http.Request) (any, int, error) {
	t, e := s.scopeRequire(st, p, r, "revoke")
	if e != nil {
		return nil, 0, e
	}
	g, ok := st.Groups[r.PathValue("group")]
	if !ok || !sameScope(g, t) {
		return nil, 0, notFound()
	}
	delete(st.Groups, g.ID)
	for id, a := range st.Assignments {
		if a.SubjectKind == "group" && a.SubjectID == g.ID {
			delete(st.Assignments, id)
		}
	}
	st.Revision++
	if e = s.ensureAdmin(st); e != nil {
		return nil, 0, e
	}
	return nil, 204, nil
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
	g, ok := st.Groups[r.PathValue("group")]
	if !ok || !sameScope(g, t) {
		return nil, 0, notFound()
	}
	user := r.PathValue("user")
	u, ok := st.Users[user]
	if !ok || (r.Method == "PUT" && u.Status != "active") {
		return nil, 0, problem(422, "INVALID_REQUEST", "Active user required")
	}
	if t.Kind == "organization" {
		if _, ok := st.Members[memberKey(t.ID, user)]; !ok {
			return nil, 0, problem(422, "INVALID_REQUEST", "Organization membership required")
		}
	}
	g.Members = remove(g.Members, user)
	if r.Method == "PUT" {
		g.Members = append(g.Members, user)
	}
	g.Revision++
	st.Groups[g.ID] = g
	st.Revision++
	if e = s.ensureAdmin(st); e != nil {
		return nil, 0, e
	}
	return g, 200, nil
}

type assignmentInput struct {
	SubjectKind string     `json:"subject_kind"`
	SubjectID   string     `json:"subject_id"`
	Role        string     `json:"role_id"`
	ScopeKind   string     `json:"scope_kind"`
	ScopeID     string     `json:"scope_id"`
	Conditions  Conditions `json:"conditions"`
	Revision    int        `json:"expected_revision,omitempty"`
}
