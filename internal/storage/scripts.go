package storage

import (
	"context"
	"slices"
	"strings"

	"github.com/krun-dev/krun/internal/domain"
)

// WorkerScripts projects the same Worker identities used by native resource
// routes into the Cloudflare list, with no independent name registry.
func (s *Store) WorkerScripts(ctx context.Context, org string) ([]domain.Resource, error) {
	out := []domain.Resource{}
	if s.pool == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, resource := range s.memory.Resources {
			if resource.Organization == org && resource.Kind == "worker" && resource.Locator != "" {
				out = append(out, resource)
			}
		}
		slices.SortFunc(out, func(a, b domain.Resource) int { return strings.Compare(a.Locator, b.Locator) })
		return out[:min(len(out), 200)], nil
	}
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
