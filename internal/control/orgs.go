package control

import (
	"net/http"
	"sort"
)

func (s *Server) orgList(st *State, p Principal, r *http.Request) (any, int, error) {
	out := []Organization{}
	for _, org := range st.Organizations {
		t, _ := resolve(st, "organization", org.ID, "global")
		d, e := Authorize(st, p, "organization.read", t)
		if e != nil {
			return nil, 0, e
		}
		if d.Allowed {
			out = append(out, org)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return items(out)
}
func (s *Server) orgCreate(st *State, p Principal, r *http.Request) (any, int, error) {
	// Self-service still requires an authenticated browser or CLI session; API
	// keys are excluded so a leaked key cannot mint new organizations.
	if !s.Config.OrganizationSelfService {
		if e := s.require(st, p, r, "organization.create", systemTarget()); e != nil {
			return nil, 0, e
		}
	} else if p.Kind != "session" && p.Kind != "cli_session" {
		return nil, 0, forbidden()
	}
	var in struct {
		Name string `json:"name"`
	}
	if e := body(r, &in); e != nil {
		return nil, 0, e
	}
	if !nameValid(in.Name) {
		return nil, 0, problem(422, "INVALID_REQUEST", "Name required (max 128 bytes)")
	}
	org := Organization{ID: randomID("org"), Name: in.Name, Sources: []string{}, Revision: 1}
	st.Organizations[org.ID] = org
	st.Members[memberKey(org.ID, p.User.ID)] = Member{org.ID, p.User.ID}
	id := randomID("role")
	st.Assignments[id] = Assignment{ID: id, SubjectKind: "user", SubjectID: p.User.ID, Role: "org_admin", ScopeKind: "organization", ScopeID: org.ID, Revision: 1}
	st.Revision++
	return org, 201, nil
}
func (s *Server) orgDetail(st *State, p Principal, r *http.Request) (any, int, error) {
	t, e := s.orgTarget(st, r)
	if e != nil {
		return nil, 0, e
	}
	if e = s.require(st, p, r, "organization.read", t); e != nil {
		if denied, ok := e.(*APIError); ok && denied.Status == 403 {
			return nil, 0, notFound()
		}
		return nil, 0, e
	}
	return st.Organizations[t.ID], 200, nil
}
func (s *Server) orgUpdate(st *State, p Principal, r *http.Request) (any, int, error) {
	t, e := s.orgTarget(st, r)
	if e != nil {
		return nil, 0, e
	}
	if e = s.require(st, p, r, "organization.update", t); e != nil {
		return nil, 0, e
	}
	var in struct {
		Name     string   `json:"name"`
		Sources  []string `json:"allowed_sources"`
		Revision int      `json:"expected_revision"`
	}
	if e = body(r, &in); e != nil {
		return nil, 0, e
	}
	if !nameValid(in.Name) {
		return nil, 0, problem(422, "INVALID_REQUEST", "Name required")
	}
	for _, src := range in.Sources {
		if !s.sourceEnabled(src) {
			return nil, 0, problem(422, "INVALID_REQUEST", "Unknown or disabled source")
		}
	}
	org := st.Organizations[t.ID]
	if in.Revision != org.Revision {
		return nil, 0, problem(409, "REVISION_CONFLICT", "Organization changed")
	}
	org.Name = in.Name
	org.Sources = in.Sources
	org.Revision++
	st.Organizations[t.ID] = org
	st.Revision++
	return org, 200, nil
}
