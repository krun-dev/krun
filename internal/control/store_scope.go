package control

import (
	"net/http"
	"strconv"
	"strings"
)

func (s *Server) scopedRequest(r *http.Request, c authCredentials) (*http.Request, error) {
	cloudflare := strings.HasPrefix(r.URL.Path, cfPrefix)
	if !cloudflare && (c.hasSession || c.keyID != "") {
		if err := s.resolveNativeRoute(r); err != nil {
			return r, err
		}
	}

	// IAM writes need the other users and grants they may affect. In particular,
	// changing organization sources checks every organization administrator.
	// Resource and Workers endpoints only need their target.
	isResource := strings.Contains(r.Pattern, "/workers") || strings.Contains(r.Pattern, "/models")
	auditRoute := strings.HasSuffix(r.Pattern, "/audit")
	simpleIAM := strings.HasPrefix(r.URL.Path, oauthPrefix+"/") ||
		r.URL.Path == "/api/auth/me" ||
		r.URL.Path == "/api/auth/logout" ||
		r.URL.Path == "/api/v1/roles" ||
		r.URL.Path == "/api/v1/orgs" ||
		r.Pattern == "GET /api/v1/orgs/{org}" ||
		r.Pattern == "GET /api/v1/orgs/{org}/permissions" ||
		r.Pattern == "GET /api/v1/system/config" ||
		r.Pattern == "GET /api/v1/system/inventory/{kind}" ||
		auditRoute
	if !isResource && !strings.Contains(r.Pattern, "/workers/") && !simpleIAM && !cloudflare {
		return r, nil
	}
	scope := stateReadScope{ActorOnly: simpleIAM || cloudflare && r.PathValue("org") == "", AllOrganizations: cloudflare && r.PathValue("org") == "" || r.URL.Path == "/api/v1/orgs" && r.Method == "GET", User: c.session.User, Key: c.keyID, Organization: r.PathValue("org"), Worker: r.PathValue("resource"), Kind: resourceKind(r), Limit: 100}
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
	// Audit and Cloudflare endpoints own their pagination contracts.
	if !auditRoute && !cloudflare {
		scope.After = r.URL.Query().Get("after")
		if raw := r.URL.Query().Get("limit"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 1 || n > 200 {
				return r, problem(422, "PAGE_INVALID", "limit must be between 1 and 200")
			}
			scope.Limit = n
		}
	}
	if scope.Worker == "" && strings.HasSuffix(r.URL.Path, "/workers") {
		scope.Worker = workerFilter(r)
	}
	if r.Pattern == "GET /api/v1/orgs/{org}/workers" {
		options, err := workerListOptions(r)
		if err != nil {
			return r, err
		}
		scope.Search = options.search
		scope.Limit = options.limit + 1
		if scope.Worker != "" {
			scope.Search = ""
			scope.After = ""
		}
	}
	scope.Summary = scope.Worker == "" || strings.HasSuffix(r.URL.Path, "/workers")
	return r.WithContext(withStateScope(r.Context(), scope)), nil
}

func resourcePageItems(st *State, r *http.Request, v any) (any, int, error) {
	out := map[string]any{"items": v}
	if scope, ok := r.Context().Value(stateReadScopeKey{}).(stateReadScope); ok && scope.Worker == "" && len(st.Resources) == scope.Limit {
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
