package storage

import (
	"context"
	"errors"
	"sort"

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
		SELECT h.host,h.org_id,h.suffix_id,h.revision,r.match_type,r.path,r.worker_id,r.enabled,''
		FROM krun_worker_hostnames h JOIN krun_worker_hostname_routes r ON r.host=h.host
		WHERE NOT h.retired AND ($1::text[] IS NULL OR h.host IN (SELECT host FROM touched_hosts))
		ORDER BY h.host,r.match_type,r.path`, ids)
}

func (s *Store) ManagedHostnames(ctx context.Context, org string) ([]domain.ManagedHostname, error) {
	return collectManagedHostnames(ctx, s.pool, `
		SELECT h.host,h.org_id,h.suffix_id,h.revision,r.match_type,r.path,r.worker_id,r.enabled,COALESCE(w.locator,'')
		FROM krun_worker_hostnames h LEFT JOIN krun_worker_hostname_routes r ON r.host=h.host
		LEFT JOIN krun_resources w ON w.resource_id=r.worker_id AND w.org_id=h.org_id AND w.kind='worker'
		WHERE h.org_id=$1 AND NOT h.retired
		ORDER BY h.host,r.match_type,r.path`, org)
}

func (s *Store) WorkerManagedHostnames(ctx context.Context, org, worker string) ([]domain.ManagedHostname, error) {
	return collectManagedHostnames(ctx, s.pool, `
		SELECT h.host,h.org_id,h.suffix_id,h.revision,r.match_type,r.path,r.worker_id,r.enabled,COALESCE(w.locator,'')
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
		var match, path, worker *string
		var enabled *bool
		var locator string
		err = rows.Scan(&item.Host, &item.Organization, &item.SuffixID, &item.Revision, &match, &path, &worker, &enabled, &locator)
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
			})
		}
	}
	return out, rows.Err()
}

func (s *Store) SaveManagedHostname(ctx context.Context, item domain.ManagedHostname, expected int, audits []domain.Audit) (domain.ManagedHostname, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return item, err
	}
	defer tx.Rollback(ctx)

	if expected == 0 {
		err = tx.QueryRow(ctx, `
			INSERT INTO krun_worker_hostnames(host,org_id,suffix_id) VALUES ($1,$2,$3)
			ON CONFLICT (host) DO NOTHING RETURNING revision`, item.Host, item.Organization, item.SuffixID).Scan(&item.Revision)
		if errors.Is(err, pgx.ErrNoRows) {
			return item, domain.Conflict("HOSTNAME_UNAVAILABLE", "Hostname is unavailable")
		}
	} else {
		err = tx.QueryRow(ctx, `
			UPDATE krun_worker_hostnames SET revision=revision+1, retired=$4
			WHERE host=$1 AND org_id=$2 AND revision=$3 AND NOT retired
			RETURNING revision`, item.Host, item.Organization, expected, item.Retired).Scan(&item.Revision)
		if errors.Is(err, pgx.ErrNoRows) {
			return item, domain.Conflict("REVISION_CONFLICT", "Hostname configuration changed")
		}
	}
	if err != nil {
		return item, err
	}

	if err = writeManagedHostnameRoutes(ctx, tx, item); err != nil {
		return item, err
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

func writeManagedHostnameRoutes(ctx context.Context, tx pgx.Tx, item domain.ManagedHostname) error {
	// Acquire dirty-row locks in worker ID order, including previous targets,
	// before changing routes. The publisher uses these same generations.
	rows, err := tx.Query(ctx, `SELECT worker_id FROM krun_worker_hostname_routes WHERE host=$1`, item.Host)
	if err != nil {
		return err
	}
	old, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, route := range item.Routes {
		old = append(old, route.Worker)
	}
	sort.Strings(old)
	for i, worker := range old {
		if i > 0 && old[i-1] == worker {
			continue
		}
		if _, err = tx.Exec(ctx, `INSERT INTO worker_config_dirty(worker_id) VALUES($1)
			ON CONFLICT(worker_id) DO UPDATE SET generation=nextval('worker_dirty_seq')`, worker); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `DELETE FROM krun_worker_hostname_routes WHERE host=$1`, item.Host); err != nil {
		return err
	}
	batch := &pgx.Batch{}
	for _, route := range item.Routes {
		batch.Queue(`INSERT INTO krun_worker_hostname_routes(host,match_type,path,worker_id,enabled)
			VALUES ($1,$2,$3,$4,$5)`, item.Host, route.Match, route.Path, route.Worker, route.Enabled)
	}
	if batch.Len() > 0 {
		return tx.SendBatch(ctx, batch).Close()
	}
	return nil
}

func (s *Store) ManagedHostname(ctx context.Context, org, host string) (domain.ManagedHostname, error) {
	items, err := collectManagedHostnames(ctx, s.pool, `
		SELECT h.host,h.org_id,h.suffix_id,h.revision,r.match_type,r.path,r.worker_id,r.enabled,COALESCE(w.locator,'')
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
