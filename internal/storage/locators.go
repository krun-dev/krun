package storage

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/uptrace/bun"
)

func insertLocator(ctx context.Context, message string, query *bun.InsertQuery) error {
	result, err := query.Exec(ctx)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return domain.Conflict("LOCATOR_CONFLICT", message)
	}
	return nil
}

func (s *Store) OrganizationID(ctx context.Context, ref string) (string, error) {
	var id string
	err := s.pool.QueryRow(ctx, `SELECT org_id FROM krun_organizations WHERE org_id=$1 OR (short_name=$1 AND short_name<>'') ORDER BY (org_id=$1) DESC LIMIT 1`, ref).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", domain.NotFound()
	}
	return id, err
}

// ResourceID resolves within the parent scope. Cloudflare accepts script names
// only; native clients can also use the explicit internal ID.
func (s *Store) ResourceID(ctx context.Context, org, kind, ref string, allowID bool) (string, error) {
	var id string
	err := s.pool.QueryRow(ctx, `
		SELECT resource_id FROM krun_resources
		WHERE org_id=$1 AND kind=$2 AND ((locator=$3 AND locator<>'') OR ($4 AND resource_id=$3))
		ORDER BY (resource_id=$3) DESC LIMIT 1`, org, kind, ref, allowID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", domain.NotFound()
	}
	return id, err
}
