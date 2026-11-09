package storage

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
)

// A scope is a query boundary, never an authorization decision. Handlers still
// authorize every result against the actor's current grants.
type ReadScope struct {
	User, Key, Organization, Worker, Kind, After string
	Search                                       string
	Limit                                        int
	Summary                                      bool
	ActorOnly, AllOrganizations                  bool
}

type ReadScopeKey struct{}

func WithReadScope(ctx context.Context, scope ReadScope) context.Context {
	return context.WithValue(ctx, ReadScopeKey{}, scope)
}

// queueStateRead preserves the shared row decoders while moving filtering into
// indexed SQL. The parameters are values; table/predicate text is internal.
func queueStateRead(ctx context.Context, b *pgx.Batch, query string) {
	if queueInvitationRead(ctx, b, query) {
		return
	}
	if queueLoginRead(ctx, b, query) {
		return
	}
	scope, ok := ctx.Value(ReadScopeKey{}).(ReadScope)
	if !ok {
		b.Queue(query)
		return
	}
	split := strings.SplitN(query, " ORDER BY ", 2)
	query = split[0]
	table := query[strings.LastIndex(query, " FROM ")+6:]
	args := []any{scope.User, scope.Organization, scope.Worker, scope.Key, scope.Kind, scope.After, scope.Limit, scope.Search}
	resources := `SELECT resource_id FROM krun_resources WHERE org_id=$2 AND kind=$5 AND ($3='' OR resource_id=$3) AND ($3<>'' OR resource_id COLLATE "C">$6 COLLATE "C") AND ($8='' OR strpos(lower(name),lower($8))>0 OR strpos(lower(resource_id),lower($8))>0 OR strpos(lower(locator),lower($8))>0) ORDER BY resource_id COLLATE "C" LIMIT $7`
	workers := `worker_id IN (` + resources + `)`
	pred := "false"
	switch table {
	case "krun_worker_buckets":
		pred = `org_id=$2`
	case "krun_worker_environment_r2":
		if !scope.Summary {
			pred = workers
		}
	case "krun_worker_configuration_r2", "krun_worker_configuration_secrets":
		if !scope.Summary {
			pred = `configuration_id IN (SELECT configuration_id FROM krun_worker_configurations WHERE ` + workers + `)`
		}
	case "krun_worker_version_r2":
		if !scope.Summary {
			pred = `version_id IN (SELECT version_id FROM krun_worker_versions WHERE ` + workers + `)`
		}
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
	case "krun_workers", "krun_worker_placements", "krun_worker_domains", "krun_worker_environments":
		pred = workers
	case "krun_worker_configurations", "krun_worker_deployments", "krun_worker_builds", "krun_worker_versions", "krun_worker_operations", "krun_worker_history", "krun_worker_secret_versions", "krun_worker_environment_secrets":
		if !scope.Summary {
			pred = workers
		}
	case "krun_worker_artifacts":
		if !scope.Summary {
			pred = `artifact_id IN (SELECT artifact_id FROM krun_worker_builds WHERE ` + workers + ` UNION SELECT artifact_id FROM krun_worker_versions WHERE ` + workers + `)`
		}
	case "krun_worker_version_secrets":
		if !scope.Summary {
			pred = `version_id IN (SELECT version_id FROM krun_worker_versions WHERE ` + workers + `)`
		}
	case "krun_worker_idempotency":
		if !scope.Summary {
			pred = `idem_key LIKE replace(replace(replace($2,'\','\\'),'_','\_'),'%','\%') || '/' || replace(replace(replace($3,'\','\\'),'_','\_'),'%','\%') || '/%'`
		}
	}
	if scope.ActorOnly {
		actor := `(subject_kind='user' AND subject_id=$1 OR subject_kind='group' AND subject_id IN(SELECT group_id FROM krun_group_members WHERE user_id=$1))`
		actorResources := `SELECT scope_id FROM krun_assignments WHERE ` + actor + ` AND scope_kind IN('worker','model')`
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
	query = `WITH scope_args AS (SELECT $1::text,$2::text,$3::text,$4::text,$5::text,$6::text,$7::int,$8::text) ` + query
	b.Queue(query, args...)
}

type LoginScope struct{ Source, Subject string }

type LoginScopeKey struct{}

func queueLoginRead(ctx context.Context, b *pgx.Batch, query string) bool {
	scope, ok := ctx.Value(LoginScopeKey{}).(LoginScope)
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
