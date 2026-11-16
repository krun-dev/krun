package storage

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/krun-dev/krun/internal/domain"
)

// A change to any target causes its entire hostname to be read in the same
// snapshot. Include old references so reassignment and retirement publish all
// affected rules in a single revision, even if the dirty queue splits workers.
const managedConfigScope = `WITH touched_hosts AS (
	SELECT host FROM krun_worker_hostname_routes WHERE worker_id=ANY($1)
	UNION SELECT host FROM worker_config_routes WHERE worker_id=ANY($1) AND route_key<>''
) `
const configRouteInScope = `(r.worker_id=ANY($1) OR (r.route_key<>'' AND r.host IN (SELECT host FROM touched_hosts)))`

func edgeManagedHostnames(ctx context.Context, tx pgx.Tx, ids []string) ([]domain.ManagedHostname, error) {
	return collectManagedHostnames(ctx, tx, managedConfigScope+`
		SELECT h.host,h.org_id,h.suffix_id,h.domain_type,h.revision,h.gateway_managed,r.match_type,r.path,r.worker_id,r.enabled,r.origin,r.cf_pattern,r.custom_domain,r.previews_enabled,''
		FROM krun_worker_hostnames h JOIN krun_worker_hostname_routes r ON r.host=h.host
		WHERE NOT h.retired AND ($1::text[] IS NULL OR h.host IN (SELECT host FROM touched_hosts))
		ORDER BY h.host,r.match_type,r.path`, ids)
}

func (s *Store) ManagedHostnames(ctx context.Context, org string) ([]domain.ManagedHostname, error) {
	return collectManagedHostnames(ctx, s.pool, `
		SELECT h.host,h.org_id,h.suffix_id,h.domain_type,h.revision,h.gateway_managed,r.match_type,r.path,r.worker_id,r.enabled,r.origin,r.cf_pattern,r.custom_domain,r.previews_enabled,COALESCE(w.locator,'')
		FROM krun_worker_hostnames h LEFT JOIN krun_worker_hostname_routes r ON r.host=h.host
		LEFT JOIN krun_resources w ON w.resource_id=r.worker_id AND w.org_id=h.org_id AND w.kind='worker'
		WHERE h.org_id=$1 AND NOT h.retired
		ORDER BY h.host,r.match_type,r.path`, org)
}

func (s *Store) WorkerManagedHostnames(ctx context.Context, org, worker string) ([]domain.ManagedHostname, error) {
	return collectManagedHostnames(ctx, s.pool, `
		SELECT h.host,h.org_id,h.suffix_id,h.domain_type,h.revision,h.gateway_managed,r.match_type,r.path,r.worker_id,r.enabled,r.origin,r.cf_pattern,r.custom_domain,r.previews_enabled,COALESCE(w.locator,'')
		FROM krun_worker_hostname_routes r JOIN krun_worker_hostnames h ON r.host=h.host
		LEFT JOIN krun_resources w ON w.resource_id=r.worker_id AND w.org_id=h.org_id AND w.kind='worker'
		WHERE r.worker_id=$1 AND h.org_id=$2 AND NOT h.retired
		ORDER BY h.host,r.match_type,r.path`, worker, org)
}

type hostnameQueryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func collectManagedHostnames(ctx context.Context, db hostnameQueryer, query string, args ...any) ([]domain.ManagedHostname, error) {
	rows, err := db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []domain.ManagedHostname{}
	for rows.Next() {
		var item domain.ManagedHostname
		var match, path, worker, origin, pattern *string
		var enabled, customDomain, previewsEnabled *bool
		var locator string
		err = rows.Scan(&item.Host, &item.Organization, &item.SuffixID, &item.Type, &item.Revision, &item.GatewayManaged, &match, &path, &worker, &enabled, &origin, &pattern, &customDomain, &previewsEnabled, &locator)
		if err != nil {
			return nil, err
		}
		if len(out) == 0 || out[len(out)-1].Host != item.Host {
			item.Routes = []domain.ManagedHostnameRoute{}
			out = append(out, item)
		}
		if match != nil {
			current := &out[len(out)-1]
			current.Routes = append(current.Routes, domain.ManagedHostnameRoute{
				Match: *match, Path: *path, Worker: *worker, WorkerLocator: locator, Enabled: *enabled,
				Origin: *origin, CFPattern: *pattern, CustomDomain: *customDomain, PreviewsEnabled: *previewsEnabled,
			})
		}
	}
	return out, rows.Err()
}

func (s *Store) SaveManagedHostname(ctx context.Context, item domain.ManagedHostname, expected int, audits []domain.Audit) (domain.ManagedHostname, error) {
	return s.saveManagedHostnames(ctx, []domain.ManagedHostname{item}, []int{expected}, audits)
}

// SaveManagedHostnames applies a script's complete route update atomically.
func (s *Store) SaveManagedHostnames(ctx context.Context, items []domain.ManagedHostname, expected []int, audits []domain.Audit) error {
	_, err := s.saveManagedHostnames(ctx, items, expected, audits)
	return err
}

func (s *Store) saveManagedHostnames(ctx context.Context, items []domain.ManagedHostname, expected []int, audits []domain.Audit) (domain.ManagedHostname, error) {
	var item domain.ManagedHostname
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return item, err
	}
	defer tx.Rollback(ctx)

	// All callers acquire hostname locks in lexical order. Worker dirty rows
	// are marked in one order after all hostname locks have been acquired.
	for i, candidate := range items {
		item, err = saveManagedHostnameRow(ctx, tx, candidate, expected[i])
		if err != nil {
			return item, err
		}
		items[i] = item
	}
	if err = markManagedHostnameWorkers(ctx, tx, items); err != nil {
		return item, err
	}
	for _, item := range items {
		if err = writeManagedHostnameRoutes(ctx, tx, item); err != nil {
			return item, err
		}
	}
	if err = insertAudits(ctx, tx, audits); err != nil {
		return item, err
	}
	if err = tx.Commit(ctx); err != nil {
		return item, err
	}
	s.SignalConfig()
	return item, nil
}

func saveManagedHostnameRow(ctx context.Context, tx pgx.Tx, item domain.ManagedHostname, revision int) (domain.ManagedHostname, error) {
	var err error

	if revision == 0 {
		err = tx.QueryRow(ctx, `
			INSERT INTO krun_worker_hostnames(host,org_id,suffix_id,gateway_managed,domain_type) VALUES ($1,$2,$3,$4,$5)
			ON CONFLICT (host) DO NOTHING RETURNING revision`, item.Host, item.Organization, item.SuffixID, item.GatewayManaged, item.Type).Scan(&item.Revision)
		if errors.Is(err, pgx.ErrNoRows) {
			return item, domain.Conflict("HOSTNAME_UNAVAILABLE", "Hostname is unavailable")
		}
	} else {
		err = tx.QueryRow(ctx, `
			UPDATE krun_worker_hostnames SET revision=revision+1, retired=$4, gateway_managed=gateway_managed OR $5
			WHERE host=$1 AND org_id=$2 AND revision=$3 AND domain_type=$6 AND suffix_id=$7 AND NOT retired
			RETURNING revision`, item.Host, item.Organization, revision, item.Retired, item.GatewayManaged, item.Type, item.SuffixID).Scan(&item.Revision)
		if errors.Is(err, pgx.ErrNoRows) {
			return item, domain.Conflict("REVISION_CONFLICT", "Hostname configuration changed")
		}
	}
	if err != nil {
		return item, err
	}

	return item, nil
}

func markManagedHostnameWorkers(ctx context.Context, tx pgx.Tx, items []domain.ManagedHostname) error {
	hosts := make([]string, 0, len(items))
	workers := []string{}
	for _, item := range items {
		hosts = append(hosts, item.Host)
		for _, route := range item.Routes {
			if route.Worker != "" {
				workers = append(workers, route.Worker)
			}
		}
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO worker_config_dirty(worker_id)
		SELECT worker_id FROM (
			SELECT unnest($2::text[]) AS worker_id
			UNION SELECT worker_id FROM krun_worker_hostname_routes WHERE host=ANY($1) AND worker_id<>''
		) dirty ORDER BY worker_id
		ON CONFLICT(worker_id) DO UPDATE SET generation=nextval('worker_dirty_seq')`, hosts, workers)
	return err
}

func writeManagedHostnameRoutes(ctx context.Context, tx pgx.Tx, item domain.ManagedHostname) error {
	if _, err := tx.Exec(ctx, `DELETE FROM krun_worker_hostname_routes WHERE host=$1`, item.Host); err != nil {
		return err
	}
	batch := &pgx.Batch{}
	for _, route := range item.Routes {
		batch.Queue(`INSERT INTO krun_worker_hostname_routes(host,match_type,path,worker_id,enabled,origin,cf_pattern,custom_domain,previews_enabled)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, item.Host, route.Match, route.Path, route.Worker, route.Enabled, route.Origin, route.CFPattern, route.CustomDomain, route.PreviewsEnabled)
	}
	if batch.Len() > 0 {
		return tx.SendBatch(ctx, batch).Close()
	}
	return nil
}

func (s *Store) ManagedHostname(ctx context.Context, org, host string) (domain.ManagedHostname, error) {
	items, err := collectManagedHostnames(ctx, s.pool, `
		SELECT h.host,h.org_id,h.suffix_id,h.domain_type,h.revision,h.gateway_managed,r.match_type,r.path,r.worker_id,r.enabled,r.origin,r.cf_pattern,r.custom_domain,r.previews_enabled,COALESCE(w.locator,'')
		FROM krun_worker_hostnames h LEFT JOIN krun_worker_hostname_routes r ON r.host=h.host
		LEFT JOIN krun_resources w ON w.resource_id=r.worker_id AND w.org_id=h.org_id AND w.kind='worker'
		WHERE h.org_id=$1 AND h.host=$2 AND NOT h.retired
		ORDER BY r.match_type,r.path`, org, host)
	if err != nil {
		return domain.ManagedHostname{}, err
	}
	if len(items) == 0 {
		return domain.ManagedHostname{}, domain.NotFound()
	}
	return items[0], nil
}

// GatewayManagedHostnames returns active and retired exact hosts. Retired
// hosts keep their deny route so the shared wildcard cannot expose stale data.
func (s *Store) GatewayManagedHostnames(ctx context.Context) ([]domain.ManagedHostname, error) {
	return collectManagedHostnames(ctx, s.pool, `
		SELECT h.host,h.org_id,h.suffix_id,h.domain_type,h.revision,h.gateway_managed,
			r.match_type,r.path,r.worker_id,r.enabled,r.origin,r.cf_pattern,r.custom_domain,r.previews_enabled,''
		FROM krun_worker_hostnames h
		LEFT JOIN krun_organizations o ON o.org_id=h.org_id
		LEFT JOIN krun_worker_hostname_routes r ON r.host=h.host AND o.org_id IS NOT NULL AND NOT h.retired
		WHERE h.gateway_managed OR r.previews_enabled
		ORDER BY h.host,r.match_type,r.path`)
}
