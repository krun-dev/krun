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
		return domain.Problem(409, "LOCATOR_CONFLICT", message)
	}
	return nil
}

func (s *Store) OrganizationID(ctx context.Context, ref string) (string, error) {
	if s.pool == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		if _, exists := s.memory.Organizations[ref]; exists {
			return ref, nil
		}
		for _, org := range s.memory.Organizations {
			if org.ShortName != "" && org.ShortName == ref {
				return org.ID, nil
			}
		}
		return "", domain.NotFound()
	}
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
	if s.pool == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		if r, ok := s.memory.Resources[ref]; allowID && ok && r.Organization == org && r.Kind == kind {
			return r.ID, nil
		}
		for _, r := range s.memory.Resources {
			if r.Organization == org && r.Kind == kind && r.Locator != "" && r.Locator == ref {
				return r.ID, nil
			}
		}
		return "", domain.NotFound()
	}
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
