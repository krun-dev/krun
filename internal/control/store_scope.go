package control

import (
	"net/http"
	"strconv"
	"strings"
)

func (s *Server) scopedRequest(r *http.Request, c authCredentials) (*http.Request, error) {
	if !s.Store.Persistent() {
		return r, nil
	}
	// IAM administration may edit other users and grants; it has a separate
	// loading contract. Resource and Workers endpoints only need their target.
	isResource := strings.Contains(r.Pattern, "/services") || strings.Contains(r.Pattern, "/models")
	cloudflare := strings.HasPrefix(r.URL.Path, cfPrefix)
	simpleIAM := strings.HasPrefix(r.URL.Path, oauthPrefix+"/") ||
		r.URL.Path == "/api/auth/me" ||
		r.URL.Path == "/api/auth/logout" ||
		r.URL.Path == "/api/v1/me" ||
		r.URL.Path == "/api/v1/roles" ||
		r.URL.Path == "/api/v1/orgs" ||
		r.Pattern == "GET /api/v1/orgs/{org}" ||
		r.Pattern == "PATCH /api/v1/orgs/{org}" ||
		r.Pattern == "GET /api/v1/orgs/{org}/permissions" ||
		strings.HasSuffix(r.Pattern, "/audit")
	if !isResource && !strings.Contains(r.Pattern, "/workers/") && !simpleIAM && !cloudflare {
		return r, nil
	}
	scope := stateReadScope{ActorOnly: simpleIAM || cloudflare && r.PathValue("org") == "", AllOrganizations: cloudflare && r.PathValue("org") == "" || r.URL.Path == "/api/v1/orgs" && r.Method == "GET", User: c.session.User, Key: c.keyID, Organization: r.PathValue("org"), Service: r.PathValue("resource"), Kind: resourceKind(r), Limit: 100, After: r.URL.Query().Get("after")}
	if c.isKey {
		owner, found, err := s.Store.APIKeyOwner(r.Context(), c.keyID)
		if err != nil {
			return r, err
		}
		if !found {
			return r, problem(401, "UNAUTHENTICATED", "Authentication required")
		}
		scope.User = owner
	}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 200 {
			return r, problem(422, "PAGE_INVALID", "limit must be between 1 and 200")
		}
		scope.Limit = n
	}
	if scope.Service == "" && strings.HasSuffix(r.URL.Path, "/workers/services") {
		scope.Service = r.URL.Query().Get("service_id")
	}
	if r.Pattern == "GET /api/v1/orgs/{org}/workers/services" {
		options, err := workerListOptions(r)
		if err != nil {
			return r, err
		}
		scope.Search = options.search
		scope.Limit = options.limit + 1
		if scope.Service != "" {
			scope.Search = ""
			scope.After = ""
		}
	}
	scope.Summary = scope.Service == "" || strings.HasSuffix(r.URL.Path, "/workers/services")
	return r.WithContext(withStateScope(r.Context(), scope)), nil
}

func resourcePageItems(st *State, r *http.Request, v any) (any, int, error) {
	out := map[string]any{"items": v}
	if scope, ok := r.Context().Value(stateReadScopeKey{}).(stateReadScope); ok && scope.Service == "" && len(st.Resources) == scope.Limit {
		after := ""
		for id := range st.Resources {
			if id > after {
				after = id
			}
		}
		out["next_after"] = after
	}
	return out, 200, nil
}
