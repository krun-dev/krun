package control

import (
	"net/http"
	"sort"
	"time"
)

func (s *Server) apiRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /api/v1/me", s.protected(false, s.me))
	m.HandleFunc("GET /api/v1/roles", s.protected(false, func(st *State, p Principal, r *http.Request) (any, int, error) {
		return map[string]any{"roles": []string{"system_admin", "org_admin", "developer", "publisher", "viewer"}, "actions": actions, "environments": environments}, 200, nil
	}))
	m.HandleFunc("POST /api/v1/authz/check", s.protected(false, s.check))
	m.HandleFunc("GET /api/v1/orgs", s.protected(false, s.orgList))
	m.HandleFunc("POST /api/v1/orgs", s.protected(true, s.orgCreate))
	m.HandleFunc("GET /api/v1/orgs/{org}", s.protected(false, s.orgDetail))
	m.HandleFunc("PATCH /api/v1/orgs/{org}", s.protected(true, s.orgUpdate))
	m.HandleFunc("GET /api/v1/system/users", s.protected(false, s.users))
	m.HandleFunc("PATCH /api/v1/system/users/{user}", s.protected(true, s.userUpdate))
	m.HandleFunc("POST /api/v1/system/invitations", s.protected(true, s.inviteHandler))
	m.HandleFunc("GET /api/v1/system/audit", s.protected(false, s.auditList))
	m.HandleFunc("GET /api/v1/orgs/{org}/audit", s.protected(false, s.auditList))
	m.HandleFunc("GET /api/v1/orgs/{org}/members", s.protected(false, s.members))
	m.HandleFunc("POST /api/v1/orgs/{org}/members", s.protected(true, s.memberAdd))
	m.HandleFunc("DELETE /api/v1/orgs/{org}/members/{user}", s.protected(true, s.memberDelete))
	for _, base := range []string{"/api/v1/orgs/{org}", "/api/v1/system"} {
		m.HandleFunc("GET "+base+"/groups", s.protected(false, s.groupList))
		m.HandleFunc("POST "+base+"/groups", s.protected(true, s.groupCreate))
		m.HandleFunc("PATCH "+base+"/groups/{group}", s.protected(true, s.groupUpdate))
		m.HandleFunc("DELETE "+base+"/groups/{group}", s.protected(true, s.groupDelete))
		m.HandleFunc("PUT "+base+"/groups/{group}/members/{user}", s.protected(true, s.groupMember))
		m.HandleFunc("DELETE "+base+"/groups/{group}/members/{user}", s.protected(true, s.groupMember))
		m.HandleFunc("GET "+base+"/role-assignments", s.protected(false, s.assignmentList))
		m.HandleFunc("POST "+base+"/role-assignments", s.protected(true, s.assignmentCreate))
		m.HandleFunc("PUT "+base+"/role-assignments/{assignment}", s.protected(true, s.assignmentUpdate))
		m.HandleFunc("DELETE "+base+"/role-assignments/{assignment}", s.protected(true, s.assignmentDelete))
		m.HandleFunc("POST "+base+"/api-keys", s.protected(true, s.keyCreate))
	}
	m.HandleFunc("GET /api/v1/system/api-keys", s.protected(false, s.keyList))
	m.HandleFunc("GET /api/v1/orgs/{org}/api-keys", s.protected(false, s.keyList))
	m.HandleFunc("GET /api/v1/api-keys/{key}", s.protected(false, s.keyDetail))
	m.HandleFunc("PATCH /api/v1/api-keys/{key}", s.protected(true, s.keyUpdate))
	m.HandleFunc("DELETE /api/v1/api-keys/{key}", s.protected(false, s.keyDelete))
	for _, kind := range []string{"services", "models"} {
		m.HandleFunc("GET /api/v1/orgs/{org}/"+kind, s.protected(false, s.resourceList))
		m.HandleFunc("POST /api/v1/orgs/{org}/"+kind, s.protected(false, s.resourceCreate))
		m.HandleFunc("GET /api/v1/orgs/{org}/"+kind+"/{resource}", s.protected(false, s.resourceDetail))
		m.HandleFunc("PATCH /api/v1/orgs/{org}/"+kind+"/{resource}", s.protected(false, s.resourceUpdate))
		m.HandleFunc("DELETE /api/v1/orgs/{org}/"+kind+"/{resource}", s.protected(false, s.resourceDelete))
	}
}
func items(v any) (any, int, error) { return map[string]any{"items": v}, 200, nil }
func systemTarget() Target          { return Target{Kind: "system", ID: "system", Environment: "global"} }
func (s *Server) orgTarget(st *State, r *http.Request) (Target, error) {
	return resolve(st, "organization", r.PathValue("org"), "global")
}
func (s *Server) scopeTarget(st *State, r *http.Request, verb string) (Target, error) {
	if r.PathValue("org") == "" {
		return systemTarget(), nil
	}
	return s.orgTarget(st, r)
}
func scopeAction(t Target, verb string) string {
	if t.Kind == "system" {
		return "system.role." + verb
	}
	switch verb {
	case "read":
		return "iam.read"
	case "grant":
		return "iam.grant"
	default:
		return "iam.revoke"
	}
}
func (s *Server) scopeRequire(st *State, p Principal, r *http.Request, verb string) (Target, error) {
	t, e := s.scopeTarget(st, r, verb)
	if e != nil {
		return t, e
	}
	return t, s.require(st, p, r, scopeAction(t, verb), t)
}
func nameValid(name string) bool { return len(name) > 0 && len(name) <= 128 }
func (s *Server) check(st *State, p Principal, r *http.Request) (any, int, error) {
	var in struct {
		Action      string `json:"action"`
		Kind        string `json:"resource_kind"`
		ID          string `json:"resource_id"`
		Environment string `json:"environment"`
	}
	if e := body(r, &in); e != nil {
		return nil, 0, e
	}
	t, e := resolve(st, in.Kind, in.ID, in.Environment)
	if e != nil {
		return nil, 0, e
	}
	d, e := Authorize(st, p, in.Action, t)
	if e != nil {
		return nil, 0, e
	}
	if !d.Allowed {
		readAction := t.Kind + ".read"
		if t.Kind == "api_key" {
			readAction = "apiKey.read"
		}
		if t.Kind == "system" {
			readAction = "system.role.read"
		}
		visible, err := Authorize(st, p, readAction, t)
		if err != nil {
			return nil, 0, err
		}
		if !visible.Allowed {
			return nil, 0, notFound()
		}
	}
	s.audit(st, p, r, in.Action, t.ID, d.Allowed)
	return d, 200, nil
}
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
	if e := s.require(st, p, r, "organization.create", systemTarget()); e != nil {
		return nil, 0, e
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
	org := Organization{ID: randomID("org"), Name: in.Name, Sources: []string{}}
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
		Name    string   `json:"name"`
		Sources []string `json:"allowed_sources"`
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
	org.Name = in.Name
	org.Sources = in.Sources
	st.Organizations[t.ID] = org
	st.Revision++
	return org, 200, nil
}
func (s *Server) users(st *State, p Principal, r *http.Request) (any, int, error) {
	if e := s.require(st, p, r, "system.user.read", systemTarget()); e != nil {
		return nil, 0, e
	}
	out := []User{}
	for _, u := range st.Users {
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return items(out)
}
func (s *Server) ensureAdmin(st *State) error {
	if !st.Bootstrapped {
		return nil
	}
	for _, u := range st.Users {
		if u.Status == "active" && s.sourceEnabled(u.Source) && isAdmin(st, u, "") {
			return nil
		}
	}
	return problem(409, "LAST_ADMIN", "At least one active system administrator must remain")
}
func (s *Server) userUpdate(st *State, p Principal, r *http.Request) (any, int, error) {
	if e := s.require(st, p, r, "system.user.update", systemTarget()); e != nil {
		return nil, 0, e
	}
	u, ok := st.Users[r.PathValue("user")]
	if !ok {
		return nil, 0, notFound()
	}
	var in struct {
		Status string `json:"status"`
	}
	if e := body(r, &in); e != nil {
		return nil, 0, e
	}
	if in.Status != "active" && in.Status != "disabled" {
		return nil, 0, problem(422, "INVALID_REQUEST", "Status must be active or disabled")
	}
	u.Status = in.Status
	u.AuthVersion++
	st.Users[u.ID] = u
	st.Revision++
	if e := s.ensureAdmin(st); e != nil {
		return nil, 0, e
	}
	return u, 200, nil
}
func (s *Server) inviteHandler(st *State, p Principal, r *http.Request) (any, int, error) {
	if e := s.require(st, p, r, "system.user.update", systemTarget()); e != nil {
		return nil, 0, e
	}
	var in struct {
		Source  string `json:"source_id"`
		Subject string `json:"subject"`
	}
	if e := body(r, &in); e != nil {
		return nil, 0, e
	}
	if !s.sourceEnabled(in.Source) || in.Subject == "" || len(in.Subject) > 512 {
		return nil, 0, problem(422, "INVALID_REQUEST", "Enabled source and stable subject required")
	}
	st.Invitations[in.Source+"\x00"+in.Subject] = now().Add(7 * 24 * time.Hour)
	st.Revision++
	return map[string]string{"status": "invited"}, 201, nil
}
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
func (s *Server) keyCreate(st *State, p Principal, r *http.Request) (any, int, error) {
	t, e := s.scopeTarget(st, r, "grant")
	if e != nil {
		return nil, 0, e
	}
	if e = s.require(st, p, r, "apiKey.create", t); e != nil {
		return nil, 0, e
	}
	var in struct {
		Name    string `json:"name"`
		Expires *int   `json:"expires_in"`
		Policy  Policy `json:"policy"`
	}
	if e = body(r, &in); e != nil {
		return nil, 0, e
	}
	if !nameValid(in.Name) || in.Expires == nil || *in.Expires < 0 || (*in.Expires > 0 && *in.Expires < 60) || *in.Expires > 90*86400 {
		return nil, 0, problem(422, "POLICY_INVALID", "Name and expires_in 0 (never) or between 60 seconds and 90 days required")
	}
	org := t.Organization
	scope := t.Kind
	if e = validatePolicy(st, p, org, scope, in.Policy); e != nil {
		return nil, 0, e
	}
	id := randomID("key")
	secret := "krun_key_" + randomToken()
	var expires *time.Time
	if *in.Expires > 0 {
		deadline := now().Add(time.Duration(*in.Expires) * time.Second)
		expires = &deadline
	}
	k := APIKey{ID: id, Owner: p.User.ID, Organization: org, ScopeKind: scope, Name: in.Name, Prefix: secret[:17], Policy: in.Policy, Expires: expires, Revision: 1}
	st.Keys[id] = k
	st.KeyHashes[id] = digest(secret)
	st.Revision++
	return map[string]any{"id": id, "key": secret, "metadata": k, "policy_revision": k.Revision, "expires_at": k.Expires}, 201, nil
}
func (s *Server) keyList(st *State, p Principal, r *http.Request) (any, int, error) {
	org := r.PathValue("org")
	if org != "" {
		if _, ok := st.Organizations[org]; !ok {
			return nil, 0, notFound()
		}
	}
	out := []APIKey{}
	for _, k := range st.Keys {
		if k.Organization != org {
			continue
		}
		t, _ := resolve(st, "api_key", k.ID, "global")
		d, e := Authorize(st, p, "apiKey.read", t)
		if e != nil {
			return nil, 0, e
		}
		if d.Allowed {
			out = append(out, k)
		}
	}
	return items(out)
}
func (s *Server) keyDetail(st *State, p Principal, r *http.Request) (any, int, error) {
	t, e := resolve(st, "api_key", r.PathValue("key"), "global")
	if e != nil {
		return nil, 0, e
	}
	if e = s.require(st, p, r, "apiKey.read", t); e != nil {
		if denied, ok := e.(*APIError); ok && denied.Status == 403 {
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
	k := st.Keys[t.ID]
	if !k.active() {
		return nil, 0, problem(409, "KEY_INACTIVE", "Key inactive")
	}
	var in struct {
		Name     string `json:"name"`
		Policy   Policy `json:"policy"`
		Revision int    `json:"expected_revision"`
	}
	if e = body(r, &in); e != nil {
		return nil, 0, e
	}
	if in.Revision != k.Revision {
		return nil, 0, problem(409, "REVISION_CONFLICT", "Key changed")
	}
	if !nameValid(in.Name) {
		return nil, 0, problem(422, "INVALID_REQUEST", "Name required")
	}
	if e = validatePolicy(st, p, k.Organization, k.ScopeKind, in.Policy); e != nil {
		return nil, 0, e
	}
	owner := st.Users[k.Owner]
	if owner.Status != "active" || !s.sourceEnabled(owner.Source) {
		return nil, 0, problem(422, "POLICY_INVALID", "Owner inactive")
	}
	if e = validatePolicy(st, Principal{User: owner, Kind: "session"}, k.Organization, k.ScopeKind, in.Policy); e != nil {
		return nil, 0, e
	}
	k.Name = in.Name
	k.Policy = in.Policy
	k.Revision++
	st.Keys[k.ID] = k
	st.Revision++
	return k, 200, nil
}
func (s *Server) keyDelete(st *State, p Principal, r *http.Request) (any, int, error) {
	t, e := resolve(st, "api_key", r.PathValue("key"), "global")
	if e != nil {
		return nil, 0, e
	}
	if e = s.require(st, p, r, "apiKey.revoke", t); e != nil {
		return nil, 0, e
	}
	k := st.Keys[t.ID]
	k.Revoked = true
	k.Revision++
	st.Keys[k.ID] = k
	st.Revision++
	return nil, 204, nil
}
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
	for id, a := range st.Assignments {
		if a.ScopeKind == t.Kind && a.ScopeID == t.ID {
			delete(st.Assignments, id)
		}
	}
	st.Revision++
	return nil, 204, nil
}
