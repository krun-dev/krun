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

	mode, ok := s.routeScopes[r.Pattern]
	if !ok {
		return r, problem(500, "INTERNAL", "route read scope is not declared")
	}
	if mode == scopeNone {
		return r, nil
	}

	scope := stateReadScope{
		User:         c.session.User,
		Key:          c.keyID,
		Organization: r.PathValue("org"),
		Worker:       r.PathValue("resource"),
		Kind:         resourceKind(r),
		Limit:        100,
	}
	switch mode {
	case scopeActor, scopeActorAudit:
		scope.ActorOnly = true
		scope.Summary = true
	case scopeActorAllOrganizations:
		scope.ActorOnly = true
		scope.AllOrganizations = true
		scope.Summary = true
	case scopeOrganization:
		// Organization catalogs and bindings do not need build, version or
		// operation rows; only the organization's Worker/resource rows.
		scope.Summary = true
	case scopeWorker:
		scope.Summary = false
	case scopeWorkerSummary:
		scope.Summary = true
	case scopeCloudflareOrganization:
		// Script-scoped endpoints need the Worker's versions, builds and
		// deployments; organization-level endpoints only need the catalog.
		scope.Summary = scope.Worker == "" || strings.HasSuffix(r.URL.Path, "/workers")
	case scopeCloudflareAccounts:
		scope.ActorOnly = true
		scope.AllOrganizations = true
		scope.Summary = true
	}
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
	if mode != scopeActorAudit && mode != scopeCloudflareOrganization && mode != scopeCloudflareAccounts {
		scope.After = r.URL.Query().Get("after")
		if raw := r.URL.Query().Get("limit"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 1 || n > 200 {
				return r, problem(422, "PAGE_INVALID", "limit must be between 1 and 200")
			}
			scope.Limit = n
		}
	}
	if mode == scopeWorkerSummary {
		scope.Worker = workerFilter(r)
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
