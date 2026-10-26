package control

import (
	"net/http"
)

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
