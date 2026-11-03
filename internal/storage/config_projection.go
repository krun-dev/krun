package storage

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/krun-dev/krun/internal/domain"
)

// WorkerConfigProjection reads only the normalized entities required by edge
// routing. It deliberately does not load users, builds, artifacts, operations,
// history or idempotency rows. A repeatable-read transaction keeps its rows consistent.
func (s *Store) WorkerConfigState(ctx context.Context, ids []string) (*domain.State, error) {

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	st := domain.NewState()
	rows, err := tx.Query(ctx, `SELECT s.service_id, s.org_id, s.network_profile
		FROM krun_worker_services s
		JOIN krun_resources r ON r.resource_id=s.service_id AND r.kind='service' AND r.org_id=s.org_id
		JOIN krun_organizations o ON o.org_id=s.org_id
		WHERE ($1::text[] IS NULL OR s.service_id=ANY($1))
		ORDER BY s.service_id`, ids)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var service, organization, profile string
		if err = rows.Scan(&service, &organization, &profile); err != nil {
			rows.Close()
			return nil, err
		}
		st.Organizations[organization] = domain.Organization{ID: organization}
		st.Resources[service] = domain.Resource{ID: service, Kind: "service", Organization: organization}
		st.Workers.Services[service] = domain.WorkerService{Service: service, Organization: organization, NetworkProfile: profile, Environments: map[string]domain.WorkerEnvironment{}}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	rows, err = tx.Query(ctx, `SELECT p.service_id, p.pool_id, p.org_id, p.network_profile,
		p.role, p.state, p.revision, p.created_at, p.updated_at
		FROM krun_worker_service_placements p
		JOIN krun_worker_services s ON s.service_id=p.service_id AND s.org_id=p.org_id
		JOIN krun_resources r ON r.resource_id=s.service_id AND r.kind='service' AND r.org_id=s.org_id
		JOIN krun_organizations o ON o.org_id=s.org_id
		WHERE ($1::text[] IS NULL OR s.service_id=ANY($1))
		ORDER BY p.service_id, p.pool_id`, ids)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var placement domain.WorkerPlacement
		if err = rows.Scan(&placement.Service, &placement.Pool, &placement.Organization, &placement.NetworkProfile,
			&placement.Role, &placement.State, &placement.Revision, &placement.Created, &placement.Updated); err != nil {
			rows.Close()
			return nil, err
		}
		st.Workers.Placements[domain.WorkerPlacementKey(placement.Service, placement.Pool)] = placement
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	rows, err = tx.Query(ctx, `SELECT d.service_id, d.domain_id
		FROM krun_worker_service_domains d
		JOIN krun_worker_services s ON s.service_id=d.service_id
		JOIN krun_resources r ON r.resource_id=s.service_id AND r.kind='service' AND r.org_id=s.org_id
		JOIN krun_organizations o ON o.org_id=s.org_id
		WHERE ($1::text[] IS NULL OR s.service_id=ANY($1))
		ORDER BY d.service_id, d.ordinal`, ids)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var service, domain string
		if err = rows.Scan(&service, &domain); err != nil {
			rows.Close()
			return nil, err
		}
		item, ok := st.Workers.Services[service]
		if ok {
			item.Domains = append(item.Domains, domain)
			st.Workers.Services[service] = item
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	rows, err = tx.Query(ctx, `SELECT e.service_id, e.env_name, e.enabled, e.version_id
		FROM krun_worker_environments e
		JOIN krun_worker_services s ON s.service_id=e.service_id
		JOIN krun_resources r ON r.resource_id=s.service_id AND r.kind='service' AND r.org_id=s.org_id
		JOIN krun_organizations o ON o.org_id=s.org_id
		WHERE e.env_name<>'@previews-base' AND ($1::text[] IS NULL OR s.service_id=ANY($1))
		ORDER BY e.service_id, e.env_name`, ids)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var service, name, version string
		var enabled bool
		if err = rows.Scan(&service, &name, &enabled, &version); err != nil {
			rows.Close()
			return nil, err
		}
		item, ok := st.Workers.Services[service]
		if ok {
			item.Environments[name] = domain.WorkerEnvironment{Name: name, Enabled: enabled, Version: version}
			st.Workers.Services[service] = item
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	rows, err = tx.Query(ctx, `SELECT rel.version_id, rel.org_id, rel.service_id, rel.env_name,
		rel.artifact_id, rel.profile, rel.archive_digest, rel.module_digest,
		rel.compatibility_date, rel.compatibility_flags, rel.config_revision,
		rel.variables, rel.secret_digest, rel.created_at
		FROM krun_worker_versions rel
		JOIN krun_worker_environments e ON (e.version_id=rel.version_id OR EXISTS(SELECT 1 FROM krun_worker_deployments d WHERE d.version_id=rel.version_id AND d.committed_at IS NOT NULL AND d.expires_at>now()))
			AND e.service_id=rel.service_id AND e.env_name=rel.env_name
		JOIN krun_worker_services s ON s.service_id=e.service_id AND s.org_id=rel.org_id
		JOIN krun_resources r ON r.resource_id=s.service_id AND r.kind='service' AND r.org_id=s.org_id
		JOIN krun_organizations o ON o.org_id=s.org_id
		WHERE ($1::text[] IS NULL OR s.service_id=ANY($1))
		ORDER BY rel.version_id`, ids)
	if err != nil {
		return nil, err
	}
	if err = readVersionRows(rows, st.Workers); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	rows, err = tx.Query(ctx, `SELECT deployment_id,org_id,service_id,env_name,version_id,operation_id,created_at,expires_at,committed_at FROM krun_worker_deployments WHERE committed_at IS NOT NULL AND expires_at>now() AND ($1::text[] IS NULL OR service_id=ANY($1))`, ids)
	if err != nil {
		return nil, err
	}
	if err = readDeploymentRows(rows, st.Workers); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return st, nil
}
