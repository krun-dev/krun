package control

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// WorkerConfigProjection reads only the normalized entities required by edge
// routing. It deliberately does not load users, builds, artifacts, operations,
// history or idempotency rows. A repeatable-read transaction keeps its rows consistent.
func (s *Store) WorkerConfigProjection(ctx context.Context, config *WorkersConfig) (workerConfigProjection, error) {

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return workerConfigProjection{}, err
	}
	defer tx.Rollback(ctx)

	st := newState()
	rows, err := queryWorkerProjection(ctx, tx, `SELECT s.service_id, s.org_id, s.network_profile
		FROM krun_worker_services s
		JOIN krun_resources r ON r.resource_id=s.service_id AND r.kind='service' AND r.org_id=s.org_id
		JOIN krun_organizations o ON o.org_id=s.org_id
		ORDER BY s.service_id`)
	if err != nil {
		return workerConfigProjection{}, err
	}
	for rows.Next() {
		var service, organization, profile string
		if err = rows.Scan(&service, &organization, &profile); err != nil {
			rows.Close()
			return workerConfigProjection{}, err
		}
		st.Organizations[organization] = Organization{ID: organization}
		st.Resources[service] = Resource{ID: service, Kind: "service", Organization: organization}
		st.Workers.Services[service] = WorkerService{Service: service, Organization: organization, NetworkProfile: profile, Environments: map[string]WorkerEnvironment{}}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return workerConfigProjection{}, err
	}
	rows.Close()

	rows, err = queryWorkerProjection(ctx, tx, `SELECT p.service_id, p.pool_id, p.org_id, p.network_profile,
		p.role, p.state, p.revision, p.created_at, p.updated_at
		FROM krun_worker_service_placements p
		JOIN krun_worker_services s ON s.service_id=p.service_id AND s.org_id=p.org_id
		JOIN krun_resources r ON r.resource_id=s.service_id AND r.kind='service' AND r.org_id=s.org_id
		JOIN krun_organizations o ON o.org_id=s.org_id
		ORDER BY p.service_id, p.pool_id`)
	if err != nil {
		return workerConfigProjection{}, err
	}
	for rows.Next() {
		var placement WorkerPlacement
		if err = rows.Scan(&placement.Service, &placement.Pool, &placement.Organization, &placement.NetworkProfile,
			&placement.Role, &placement.State, &placement.Revision, &placement.Created, &placement.Updated); err != nil {
			rows.Close()
			return workerConfigProjection{}, err
		}
		st.Workers.Placements[workerPlacementKey(placement.Service, placement.Pool)] = placement
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return workerConfigProjection{}, err
	}
	rows.Close()

	rows, err = queryWorkerProjection(ctx, tx, `SELECT d.service_id, d.domain_id
		FROM krun_worker_service_domains d
		JOIN krun_worker_services s ON s.service_id=d.service_id
		JOIN krun_resources r ON r.resource_id=s.service_id AND r.kind='service' AND r.org_id=s.org_id
		JOIN krun_organizations o ON o.org_id=s.org_id
		ORDER BY d.service_id, d.ordinal`)
	if err != nil {
		return workerConfigProjection{}, err
	}
	for rows.Next() {
		var service, domain string
		if err = rows.Scan(&service, &domain); err != nil {
			rows.Close()
			return workerConfigProjection{}, err
		}
		item, ok := st.Workers.Services[service]
		if ok {
			item.Domains = append(item.Domains, domain)
			st.Workers.Services[service] = item
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return workerConfigProjection{}, err
	}
	rows.Close()

	rows, err = queryWorkerProjection(ctx, tx, `SELECT e.service_id, e.env_name, e.enabled, e.release_id
		FROM krun_worker_environments e
		JOIN krun_worker_services s ON s.service_id=e.service_id
		JOIN krun_resources r ON r.resource_id=s.service_id AND r.kind='service' AND r.org_id=s.org_id
		JOIN krun_organizations o ON o.org_id=s.org_id
		ORDER BY e.service_id, e.env_name`)
	if err != nil {
		return workerConfigProjection{}, err
	}
	for rows.Next() {
		var service, name, release string
		var enabled bool
		if err = rows.Scan(&service, &name, &enabled, &release); err != nil {
			rows.Close()
			return workerConfigProjection{}, err
		}
		item, ok := st.Workers.Services[service]
		if ok {
			item.Environments[name] = WorkerEnvironment{Name: name, Enabled: enabled, Release: release}
			st.Workers.Services[service] = item
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return workerConfigProjection{}, err
	}
	rows.Close()

	rows, err = queryWorkerProjection(ctx, tx, `SELECT rel.release_id, rel.org_id, rel.service_id, rel.env_name,
		rel.artifact_id, rel.profile, rel.archive_digest, rel.module_digest,
		rel.compatibility_date, rel.compatibility_flags, rel.config_revision,
		rel.variables, rel.secret_digest, rel.created_at
		FROM krun_worker_releases rel
		JOIN krun_worker_environments e ON e.release_id=rel.release_id
			AND e.service_id=rel.service_id AND e.env_name=rel.env_name
		JOIN krun_worker_services s ON s.service_id=e.service_id AND s.org_id=rel.org_id
		JOIN krun_resources r ON r.resource_id=s.service_id AND r.kind='service' AND r.org_id=s.org_id
		JOIN krun_organizations o ON o.org_id=s.org_id
		ORDER BY rel.release_id`)
	if err != nil {
		return workerConfigProjection{}, err
	}
	if err = readReleaseRows(rows, st.Workers); err != nil {
		rows.Close()
		return workerConfigProjection{}, err
	}
	rows.Close()
	if err = tx.Commit(ctx); err != nil {
		return workerConfigProjection{}, err
	}
	return workerConfigFromState(st, config), nil
}
