package storage

import (
	"context"

	"github.com/krun-dev/krun/internal/domain"
)

// WorkerScripts projects the same Worker identities used by native resource
// routes into the Cloudflare list, with no independent name registry.
func (s *Store) WorkerScripts(ctx context.Context, org string) ([]domain.Resource, error) {
	out := []domain.Resource{}

	rows, err := s.pool.Query(ctx, `SELECT resource_id, locator FROM krun_resources WHERE org_id=$1 AND kind='worker' AND locator<>'' ORDER BY locator LIMIT 200`, org)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		resource := domain.Resource{Organization: org, Kind: "worker"}
		if err := rows.Scan(&resource.ID, &resource.Locator); err != nil {
			return nil, err
		}
		out = append(out, resource)
	}
	return out, rows.Err()
}
