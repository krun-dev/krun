package control

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

// A scope is a query boundary, never an authorization decision. Handlers still
// authorize every result against the actor's current grants.
type stateReadScope struct {
	User, Key, Organization, Service, Kind, After string
	Limit                                         int
	Summary                                       bool
	ActorOnly, AllOrganizations                   bool
}
type stateReadScopeKey struct{}

func withStateScope(ctx context.Context, scope stateReadScope) context.Context {
	return context.WithValue(ctx, stateReadScopeKey{}, scope)
}

func (s *Server) scopedRequest(r *http.Request, c authCredentials) (*http.Request, error) {
	if s.Store.pool == nil {
		return r, nil
	}
	// IAM administration may edit other users and grants; it has a separate
	// loading contract. Resource and Workers endpoints only need their target.
	isResource := strings.Contains(r.Pattern, "/services") || strings.Contains(r.Pattern, "/models")
	simpleIAM := r.URL.Path == "/api/auth/me" || r.URL.Path == "/api/auth/logout" || r.URL.Path == "/api/v1/me" || r.URL.Path == "/api/v1/roles" || r.URL.Path == "/api/v1/orgs" || r.Pattern == "GET /api/v1/orgs/{org}" || r.Pattern == "PATCH /api/v1/orgs/{org}" || strings.HasSuffix(r.Pattern, "/audit")
	if !isResource && !strings.Contains(r.Pattern, "/workers/") && !simpleIAM {
		return r, nil
	}
	scope := stateReadScope{ActorOnly: simpleIAM, AllOrganizations: r.URL.Path == "/api/v1/orgs" && r.Method == "GET", User: c.session.User, Key: c.keyID, Organization: r.PathValue("org"), Service: r.PathValue("resource"), Kind: resourceKind(r), Limit: 100, After: r.URL.Query().Get("after")}
	if c.isKey {
		if err := s.Store.pool.QueryRow(r.Context(), `SELECT owner_user_id FROM krun_api_keys WHERE key_id=$1`, c.keyID).Scan(&scope.User); err != nil {
			if err == pgx.ErrNoRows {
				return r, problem(401, "UNAUTHENTICATED", "Authentication required")
			}
			return r, err
		}
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
	scope.Summary = scope.Service == "" || strings.HasSuffix(r.URL.Path, "/workers/services")
	return r.WithContext(withStateScope(r.Context(), scope)), nil
}

// queueStateRead preserves the shared row decoders while moving filtering into
// indexed SQL. The parameters are values; table/predicate text is internal.
func queueStateRead(ctx context.Context, b *pgx.Batch, query string) {
	if queueLoginRead(ctx, b, query) {
		return
	}
	scope, ok := ctx.Value(stateReadScopeKey{}).(stateReadScope)
	if !ok {
		b.Queue(query)
		return
	}
	split := strings.SplitN(query, " ORDER BY ", 2)
	query = split[0]
	table := query[strings.LastIndex(query, " FROM ")+6:]
	args := []any{scope.User, scope.Organization, scope.Service, scope.Key, scope.Kind, scope.After, scope.Limit}
	resources := `SELECT resource_id FROM krun_resources WHERE org_id=$2 AND kind=$5 AND ($3='' OR resource_id=$3) AND ($3<>'' OR resource_id>$6) ORDER BY resource_id LIMIT $7`
	services := `service_id IN (` + resources + `)`
	pred := "false"
	switch table {
	case "krun_users":
		pred = `user_id=$1`
	case "krun_organizations", "krun_org_sources":
		pred = `org_id=$2`
	case "krun_members":
		pred = `user_id=$1 AND org_id=$2`
	case "krun_groups":
		pred = `group_id IN (SELECT group_id FROM krun_group_members WHERE user_id=$1) AND (scope_kind='system' OR scope_id=$2)`
	case "krun_group_members":
		pred = `user_id=$1`
	case "krun_assignments":
		pred = `(subject_kind='user' AND subject_id=$1 OR subject_kind='group' AND subject_id IN (SELECT group_id FROM krun_group_members WHERE user_id=$1)) AND (scope_kind='system' OR scope_kind='organization' AND scope_id=$2 OR scope_id IN (` + resources + `))`
	case "krun_resources":
		pred = `resource_id IN (` + resources + `)`
	case "krun_api_keys":
		pred = `key_id=$4`
	case "krun_identity_sources", "krun_identity_providers", "krun_meta":
		pred = `true`
	case "krun_worker_services", "krun_worker_service_placements", "krun_worker_service_domains", "krun_worker_environments":
		pred = services
	case "krun_worker_builds", "krun_worker_releases", "krun_worker_operations", "krun_worker_history", "krun_worker_secret_versions", "krun_worker_environment_secrets":
		if !scope.Summary {
			pred = services
		}
	case "krun_worker_artifacts":
		if !scope.Summary {
			pred = `artifact_id IN (SELECT artifact_id FROM krun_worker_builds WHERE ` + services + ` UNION SELECT artifact_id FROM krun_worker_releases WHERE ` + services + `)`
		}
	case "krun_worker_release_secrets":
		if !scope.Summary {
			pred = `release_id IN (SELECT release_id FROM krun_worker_releases WHERE ` + services + `)`
		}
	case "krun_worker_idempotency":
		if !scope.Summary {
			pred = `idem_key LIKE replace(replace(replace($2,'\','\\'),'_','\_'),'%','\%') || '/' || replace(replace(replace($3,'\','\\'),'_','\_'),'%','\%') || '/%'`
		}
	}
	if scope.ActorOnly {
		actor := `(subject_kind='user' AND subject_id=$1 OR subject_kind='group' AND subject_id IN(SELECT group_id FROM krun_group_members WHERE user_id=$1))`
		actorResources := `SELECT scope_id FROM krun_assignments WHERE ` + actor + ` AND scope_kind IN('service','model')`
		switch table {
		case "krun_resources":
			pred = `resource_id IN(` + actorResources + `)`
			if scope.Organization != "" {
				pred += ` AND org_id=$2`
			}
		case "krun_assignments":
			pred = actor
		case "krun_groups":
			pred = `group_id IN(SELECT group_id FROM krun_group_members WHERE user_id=$1)`
		case "krun_members":
			pred = `user_id=$1`
			if !scope.AllOrganizations {
				pred += ` AND org_id=$2`
			}
		case "krun_organizations", "krun_org_sources":
			if scope.AllOrganizations {
				pred = `(org_id IN(SELECT org_id FROM krun_members WHERE user_id=$1) OR EXISTS(SELECT 1 FROM krun_assignments WHERE ` + actor + ` AND scope_kind='system' AND role='system_admin'))`
			}
		}
	}
	query += " WHERE " + pred
	if len(split) > 1 {
		query += " ORDER BY " + split[1]
	}
	// pgx rejects unused bind arguments. A typed VALUES row makes the common
	// argument contract explicit, including for deliberately empty tables.
	query = `WITH scope_args AS (SELECT $1::text,$2::text,$3::text,$4::text,$5::text,$6::text,$7::int) ` + query
	b.Queue(query, args...)
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

type loginReadScope struct{ Source, Subject string }
type loginReadScopeKey struct{}

func queueLoginRead(ctx context.Context, b *pgx.Batch, query string) bool {
	scope, ok := ctx.Value(loginReadScopeKey{}).(loginReadScope)
	if !ok {
		return false
	}
	parts := strings.SplitN(query, " ORDER BY ", 2)
	base := parts[0]
	table := base[strings.LastIndex(base, " FROM ")+6:]
	switch table {
	case "krun_users", "krun_invitations":
		base += " WHERE source=$1 AND subject=$2"
		if len(parts) > 1 {
			base += " ORDER BY " + parts[1]
		}
		b.Queue(base, scope.Source, scope.Subject)
	case "krun_identity_sources", "krun_identity_providers", "krun_meta":
		b.Queue(query)
	default:
		base += " WHERE false"
		if len(parts) > 1 {
			base += " ORDER BY " + parts[1]
		}
		b.Queue(base)
	}
	return true
}
