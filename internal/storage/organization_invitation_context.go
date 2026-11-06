package storage

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/krun-dev/krun/internal/domain"
)

type invitationReadScope struct {
	organization string
	users        []string
}

type invitationReadScopeKey struct{}

// OrganizationInvitationContext reads only this organization's authorization
// facts and the supplied actor, creator and optional directed recipient.
func (s *Store) OrganizationInvitationContext(ctx context.Context, organization string, users ...string) (*domain.State, error) {
	if s.pool == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		return domain.CloneState(s.memory)
	}
	ctx = context.WithValue(ctx, invitationReadScopeKey{}, invitationReadScope{organization: organization, users: users})
	st := domain.NewState()
	if err := s.loadIAM(ctx, st); err != nil {
		return nil, err
	}
	return st, nil
}

func queueInvitationRead(ctx context.Context, batch *pgx.Batch, query string) bool {
	scope, ok := ctx.Value(invitationReadScopeKey{}).(invitationReadScope)
	if !ok {
		return false
	}
	parts := strings.SplitN(query, " ORDER BY ", 2)
	base := parts[0]
	table := base[strings.LastIndex(base, " FROM ")+6:]
	groups := `SELECT group_id FROM krun_group_members WHERE user_id=ANY($2::text[])`
	subjects := `(subject_kind='user' AND subject_id=ANY($2::text[]) OR subject_kind='group' AND subject_id IN (` + groups + `))`
	predicate := "false"
	switch table {
	case "krun_users":
		predicate = `user_id=ANY($2::text[])`
	case "krun_organizations", "krun_org_sources":
		predicate = `org_id=$1`
	case "krun_members":
		predicate = `org_id=$1 AND user_id=ANY($2::text[])`
	case "krun_groups":
		predicate = `group_id IN (` + groups + `) AND (scope_kind='system' OR scope_id=$1)`
	case "krun_group_members":
		predicate = `user_id=ANY($2::text[])`
	case "krun_assignments":
		predicate = subjects + ` AND (scope_kind='system' OR scope_kind='organization' AND scope_id=$1 OR scope_kind IN ('service','model') AND scope_id IN (SELECT resource_id FROM krun_resources WHERE org_id=$1))`
	case "krun_resources":
		predicate = `org_id=$1 AND resource_id IN (SELECT scope_id FROM krun_assignments WHERE ` + subjects + ` AND scope_kind IN ('service','model'))`
	case "krun_identity_sources", "krun_identity_providers", "krun_meta":
		predicate = "true"
	}
	base = `WITH invitation_scope AS (SELECT $1::text, $2::text[]) ` + base + " WHERE " + predicate
	if len(parts) > 1 {
		base += " ORDER BY " + parts[1]
	}
	batch.Queue(base, scope.organization, scope.users)
	return true
}
