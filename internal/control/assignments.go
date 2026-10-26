package control

import (
	"net/http"
)

func validateAssignment(st *State, t Target, in assignmentInput) error {
	if t.Kind == "system" {
		if in.Role != "system_admin" || in.ScopeKind != "system" || in.ScopeID != "system" || len(in.Conditions.Environments) > 0 {
			return problem(422, "POLICY_INVALID", "System scope permits only system_admin")
		}
	} else {
		if in.Role != "org_admin" && roleActions[in.Role] == nil {
			return problem(422, "POLICY_INVALID", "Unknown organization role")
		}
		if in.ScopeKind == "organization" {
			if in.ScopeID != t.ID {
				return problem(422, "POLICY_INVALID", "Wrong organization scope")
			}
		} else {
			res, ok := st.Resources[in.ScopeID]
			if !ok || res.Organization != t.ID || res.Kind != in.ScopeKind || in.Role == "org_admin" {
				return problem(422, "POLICY_INVALID", "Invalid resource scope")
			}
		}
		if in.Role == "org_admin" && len(in.Conditions.Environments) > 0 {
			return problem(422, "POLICY_INVALID", "Administrator roles cannot have environment conditions")
		}
	}
	for _, env := range in.Conditions.Environments {
		if !contains(environments, env) {
			return problem(422, "POLICY_INVALID", "Unknown environment")
		}
	}
	if in.SubjectKind == "user" {
		u, ok := st.Users[in.SubjectID]
		if !ok || u.Status != "active" {
			return problem(422, "POLICY_INVALID", "Active user required")
		}
		if t.Kind == "organization" {
			if _, ok := st.Members[memberKey(t.ID, u.ID)]; !ok {
				return problem(422, "POLICY_INVALID", "Organization membership required")
			}
		}
	} else if in.SubjectKind == "group" {
		g, ok := st.Groups[in.SubjectID]
		if !ok || !sameScope(g, t) {
			return problem(422, "POLICY_INVALID", "Group must belong to this scope")
		}
	} else {
		return problem(422, "POLICY_INVALID", "Subject must be user or group")
	}
	return nil
}
func assignmentScope(st *State, a Assignment, t Target) bool {
	if t.Kind == "system" {
		return a.ScopeKind == "system"
	}
	return a.ScopeKind != "system" && scopeOrg(st, a) == t.ID
}
func (s *Server) assignmentList(st *State, p Principal, r *http.Request) (any, int, error) {
	t, e := s.scopeRequire(st, p, r, "read")
	if e != nil {
		return nil, 0, e
	}
	out := []Assignment{}
	for _, a := range st.Assignments {
		if assignmentScope(st, a, t) {
			out = append(out, a)
		}
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
	var in assignmentInput
	if e = body(r, &in); e != nil {
		return nil, 0, e
	}
	if e = validateAssignment(st, t, in); e != nil {
		return nil, 0, e
	}
	id := randomID("role")
	rev := 1
	if update {
		id = r.PathValue("assignment")
		old, ok := st.Assignments[id]
		if !ok || !assignmentScope(st, old, t) {
			return nil, 0, notFound()
		}
		if in.Revision != old.Revision {
			return nil, 0, problem(409, "REVISION_CONFLICT", "Assignment changed")
		}
		rev = old.Revision + 1
	}
	a := Assignment{ID: id, SubjectKind: in.SubjectKind, SubjectID: in.SubjectID, Role: in.Role, ScopeKind: in.ScopeKind, ScopeID: in.ScopeID, Conditions: in.Conditions, Revision: rev}
	st.Assignments[id] = a
	st.Revision++
	if e = s.ensureAdmin(st); e != nil {
		return nil, 0, e
	}
	return a, 201, nil
}
func (s *Server) assignmentDelete(st *State, p Principal, r *http.Request) (any, int, error) {
	t, e := s.scopeRequire(st, p, r, "revoke")
	if e != nil {
		return nil, 0, e
	}
	a, ok := st.Assignments[r.PathValue("assignment")]
	if !ok || !assignmentScope(st, a, t) {
		return nil, 0, notFound()
	}
	delete(st.Assignments, a.ID)
	st.Revision++
	if e = s.ensureAdmin(st); e != nil {
		return nil, 0, e
	}
	return nil, 204, nil
}
