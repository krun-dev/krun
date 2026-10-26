package control

import (
	"net/http"
)

func (s *Server) members(st *State, p Principal, r *http.Request) (any, int, error) {
	t, e := s.orgTarget(st, r)
	if e != nil {
		return nil, 0, e
	}
	if e = s.require(st, p, r, "member.read", t); e != nil {
		return nil, 0, e
	}
	out := []User{}
	for _, m := range st.Members {
		if m.Organization == t.ID {
			out = append(out, st.Users[m.User])
		}
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
	var in struct {
		User string `json:"user_id"`
	}
	if e = body(r, &in); e != nil {
		return nil, 0, e
	}
	u, ok := st.Users[in.User]
	if !ok || u.Status != "active" {
		return nil, 0, problem(422, "INVALID_REQUEST", "Existing active local user_id required")
	}
	m := Member{t.ID, u.ID}
	st.Members[memberKey(t.ID, u.ID)] = m
	st.Revision++
	return m, 201, nil
}
func (s *Server) memberDelete(st *State, p Principal, r *http.Request) (any, int, error) {
	t, e := s.orgTarget(st, r)
	if e != nil {
		return nil, 0, e
	}
	if e = s.require(st, p, r, "member.manage", t); e != nil {
		return nil, 0, e
	}
	user := r.PathValue("user")
	if _, ok := st.Members[memberKey(t.ID, user)]; !ok {
		return nil, 0, notFound()
	}
	delete(st.Members, memberKey(t.ID, user))
	for id, g := range st.Groups {
		if g.ScopeKind == "organization" && g.ScopeID == t.ID {
			g.Members = remove(g.Members, user)
			g.Revision++
			st.Groups[id] = g
		}
	}
	for id, a := range st.Assignments {
		if a.SubjectKind == "user" && a.SubjectID == user && scopeOrg(st, a) == t.ID {
			delete(st.Assignments, id)
		}
	}
	st.Revision++
	return nil, 204, nil
}
