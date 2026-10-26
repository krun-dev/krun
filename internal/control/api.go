package control

import "net/http"

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
