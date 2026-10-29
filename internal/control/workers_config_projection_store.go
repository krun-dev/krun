package control

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
)

// replacePublishedWorkerConfig stores only the current edge entities. Revision
// rows retain the bounded delta stream, while the full immutable snapshots live
// in object storage. Each JSON payload below describes one release entity, not
// a multi-entity state document.
func replacePublishedWorkerConfig(ctx context.Context, tx pgx.Tx, payload []byte) error {
	raw := string(payload)
	if _, err := tx.Exec(ctx, `WITH incoming AS MATERIALIZED (
			SELECT route->>'host' AS host,
				COALESCE(NULLIF(route->>'pool',''),'default') AS pool,
				COALESCE(route->>'path','') AS path,
				COALESCE((route->>'enabled')::boolean,false) AS enabled,
				COALESCE(route->>'release','') AS release_id
			FROM jsonb_array_elements(COALESCE($1::jsonb->'routes','[]'::jsonb)) AS route
		), upserted AS (
			INSERT INTO worker_config_routes (host, pool, path, enabled, release_id)
			SELECT host, pool, path, enabled, release_id FROM incoming
			ON CONFLICT (host) DO UPDATE SET pool=excluded.pool, path=excluded.path, enabled=excluded.enabled, release_id=excluded.release_id
			RETURNING host
		)
		DELETE FROM worker_config_routes current
		WHERE NOT EXISTS (SELECT 1 FROM incoming WHERE incoming.host=current.host)`, raw); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `WITH incoming AS MATERIALIZED (
			SELECT DISTINCT item.key AS release_id,
				COALESCE(NULLIF(route->>'pool',''),'default') AS pool, item.value AS payload
			FROM jsonb_array_elements(COALESCE($1::jsonb->'routes','[]'::jsonb)) AS route
			JOIN jsonb_each(COALESCE($1::jsonb->'releases','{}'::jsonb)) AS item
				ON item.key=route->>'release'
		), upserted AS (
			INSERT INTO worker_config_releases (release_id, pool, payload)
			SELECT release_id, pool, payload FROM incoming
			ON CONFLICT (release_id, pool) DO UPDATE SET payload=excluded.payload
			RETURNING release_id, pool
		)
		DELETE FROM worker_config_releases current
		WHERE NOT EXISTS (SELECT 1 FROM incoming WHERE incoming.release_id=current.release_id AND incoming.pool=current.pool)`, raw); err != nil {
		return err
	}
	return nil
}

// WorkerTopologyGeneration is the cheap publisher gate. Memory stores return
// -1 so tests keep observing every mutation without maintaining a second
// generation implementation.
func (s *Store) WorkerTopologyGeneration(ctx context.Context) (int64, error) {
	if s.pool == nil {
		return -1, nil
	}
	var generation int64
	err := s.pool.QueryRow(ctx, `SELECT COALESCE((SELECT value::bigint FROM krun_meta WHERE key='worker_topology_generation'),0)`).Scan(&generation)
	return generation, err
}

// WorkerConfigProjection reads only the normalized entities required by edge
// routing. It deliberately does not load users, builds, artifacts, operations,
// history or idempotency rows. The repeatable-read transaction couples the
// returned generation to the exact projection it describes.
func (s *Store) WorkerConfigProjection(ctx context.Context, config *WorkersConfig) (workerConfigProjection, int64, error) {
	if s.pool == nil {
		var projection workerConfigProjection
		err := s.View(ctx, PartitionAll, func(st *State) error {
			projection = workerConfigFromState(st, config)
			return nil
		})
		return projection, -1, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return workerConfigProjection{}, 0, err
	}
	defer tx.Rollback(ctx)

	var generation int64
	if err = tx.QueryRow(ctx, `SELECT COALESCE((SELECT value::bigint FROM krun_meta WHERE key='worker_topology_generation'),0)`).Scan(&generation); err != nil {
		return workerConfigProjection{}, 0, err
	}
	st := newState()
	rows, err := tx.Query(ctx, `SELECT s.service_id, s.org_id, s.network_profile
		FROM krun_worker_services s
		JOIN krun_resources r ON r.resource_id=s.service_id AND r.kind='service' AND r.org_id=s.org_id
		JOIN krun_organizations o ON o.org_id=s.org_id
		ORDER BY s.service_id`)
	if err != nil {
		return workerConfigProjection{}, 0, err
	}
	for rows.Next() {
		var service, organization, profile string
		if err = rows.Scan(&service, &organization, &profile); err != nil {
			rows.Close()
			return workerConfigProjection{}, 0, err
		}
		st.Organizations[organization] = Organization{ID: organization}
		st.Resources[service] = Resource{ID: service, Kind: "service", Organization: organization}
		st.Workers.Services[service] = WorkerService{Service: service, Organization: organization, NetworkProfile: profile, Environments: map[string]WorkerEnvironment{}}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return workerConfigProjection{}, 0, err
	}
	rows.Close()

	rows, err = tx.Query(ctx, `SELECT p.service_id, p.pool_id, p.org_id, p.network_profile,
		p.role, p.state, p.revision, p.created_at, p.updated_at
		FROM krun_worker_service_placements p
		JOIN krun_worker_services s ON s.service_id=p.service_id AND s.org_id=p.org_id
		JOIN krun_resources r ON r.resource_id=s.service_id AND r.kind='service' AND r.org_id=s.org_id
		JOIN krun_organizations o ON o.org_id=s.org_id
		ORDER BY p.service_id, p.pool_id`)
	if err != nil {
		return workerConfigProjection{}, 0, err
	}
	for rows.Next() {
		var placement WorkerPlacement
		if err = rows.Scan(&placement.Service, &placement.Pool, &placement.Organization, &placement.NetworkProfile,
			&placement.Role, &placement.State, &placement.Revision, &placement.Created, &placement.Updated); err != nil {
			rows.Close()
			return workerConfigProjection{}, 0, err
		}
		st.Workers.Placements[workerPlacementKey(placement.Service, placement.Pool)] = placement
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return workerConfigProjection{}, 0, err
	}
	rows.Close()

	rows, err = tx.Query(ctx, `SELECT d.service_id, d.domain_id
		FROM krun_worker_service_domains d
		JOIN krun_worker_services s ON s.service_id=d.service_id
		JOIN krun_resources r ON r.resource_id=s.service_id AND r.kind='service' AND r.org_id=s.org_id
		JOIN krun_organizations o ON o.org_id=s.org_id
		ORDER BY d.service_id, d.ordinal`)
	if err != nil {
		return workerConfigProjection{}, 0, err
	}
	for rows.Next() {
		var service, domain string
		if err = rows.Scan(&service, &domain); err != nil {
			rows.Close()
			return workerConfigProjection{}, 0, err
		}
		item, ok := st.Workers.Services[service]
		if ok {
			item.Domains = append(item.Domains, domain)
			st.Workers.Services[service] = item
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return workerConfigProjection{}, 0, err
	}
	rows.Close()

	rows, err = tx.Query(ctx, `SELECT e.service_id, e.env_name, e.enabled, e.release_id
		FROM krun_worker_environments e
		JOIN krun_worker_services s ON s.service_id=e.service_id
		JOIN krun_resources r ON r.resource_id=s.service_id AND r.kind='service' AND r.org_id=s.org_id
		JOIN krun_organizations o ON o.org_id=s.org_id
		ORDER BY e.service_id, e.env_name`)
	if err != nil {
		return workerConfigProjection{}, 0, err
	}
	for rows.Next() {
		var service, name, release string
		var enabled bool
		if err = rows.Scan(&service, &name, &enabled, &release); err != nil {
			rows.Close()
			return workerConfigProjection{}, 0, err
		}
		item, ok := st.Workers.Services[service]
		if ok {
			item.Environments[name] = WorkerEnvironment{Name: name, Enabled: enabled, Release: release}
			st.Workers.Services[service] = item
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return workerConfigProjection{}, 0, err
	}
	rows.Close()

	rows, err = tx.Query(ctx, `SELECT rel.release_id, rel.org_id, rel.service_id, rel.env_name,
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
		return workerConfigProjection{}, 0, err
	}
	if err = readReleaseRows(rows, st.Workers); err != nil {
		rows.Close()
		return workerConfigProjection{}, 0, err
	}
	rows.Close()
	if err = tx.Commit(ctx); err != nil {
		return workerConfigProjection{}, 0, err
	}
	return workerConfigFromState(st, config), generation, nil
}

// WorkerRoutingTopology is the HTTPRoute reconciler's compact read model. It
// excludes releases as well as all build/operation/IAM detail; organization and
// resource joins in the queries filter out deleted services without loading
// either catalogue into Go.
type workerRoutingService struct {
	Service WorkerService
	Pool    string
}

func (s *Store) WorkerRoutingTopology(ctx context.Context) (map[string]workerRoutingService, int64, error) {
	if s.pool == nil {
		services := map[string]workerRoutingService{}
		err := s.View(ctx, PartitionAll, func(st *State) error {
			for id, service := range st.Workers.Services {
				resource, live := st.Resources[id]
				if live && resource.Kind == "service" && resource.Organization == service.Organization {
					if placement, ok := activeWorkerPlacement(st.Workers, id); ok {
						services[id] = workerRoutingService{Service: service, Pool: placement.Pool}
					}
				}
			}
			return nil
		})
		return services, -1, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback(ctx)
	var generation int64
	if err = tx.QueryRow(ctx, `SELECT COALESCE((SELECT value::bigint FROM krun_meta WHERE key='worker_topology_generation'),0)`).Scan(&generation); err != nil {
		return nil, 0, err
	}
	services := map[string]workerRoutingService{}
	rows, err := tx.Query(ctx, `SELECT s.service_id, s.org_id, s.network_profile, p.pool_id
		FROM krun_worker_services s
		JOIN krun_worker_service_placements p ON p.service_id=s.service_id AND p.org_id=s.org_id
			AND p.role='primary' AND p.state='active'
		JOIN krun_resources r ON r.resource_id=s.service_id AND r.kind='service' AND r.org_id=s.org_id
		JOIN krun_organizations o ON o.org_id=s.org_id
		ORDER BY s.service_id`)
	if err != nil {
		return nil, 0, err
	}
	for rows.Next() {
		var service, organization, profile, pool string
		if err = rows.Scan(&service, &organization, &profile, &pool); err != nil {
			rows.Close()
			return nil, 0, err
		}
		services[service] = workerRoutingService{Service: WorkerService{Service: service, Organization: organization, NetworkProfile: profile, Environments: map[string]WorkerEnvironment{}}, Pool: pool}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, 0, err
	}
	rows.Close()
	rows, err = tx.Query(ctx, `SELECT d.service_id, d.domain_id
		FROM krun_worker_service_domains d
		JOIN krun_worker_services s ON s.service_id=d.service_id
		JOIN krun_resources r ON r.resource_id=s.service_id AND r.kind='service' AND r.org_id=s.org_id
		JOIN krun_organizations o ON o.org_id=s.org_id
		ORDER BY d.service_id, d.ordinal`)
	if err != nil {
		return nil, 0, err
	}
	for rows.Next() {
		var service, domain string
		if err = rows.Scan(&service, &domain); err != nil {
			rows.Close()
			return nil, 0, err
		}
		item, ok := services[service]
		if ok {
			item.Service.Domains = append(item.Service.Domains, domain)
			services[service] = item
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, 0, err
	}
	rows.Close()
	rows, err = tx.Query(ctx, `SELECT e.service_id, e.env_name, e.enabled, e.release_id
		FROM krun_worker_environments e
		JOIN krun_worker_services s ON s.service_id=e.service_id
		JOIN krun_resources r ON r.resource_id=s.service_id AND r.kind='service' AND r.org_id=s.org_id
		JOIN krun_organizations o ON o.org_id=s.org_id
		ORDER BY e.service_id, e.env_name`)
	if err != nil {
		return nil, 0, err
	}
	for rows.Next() {
		var service, name, release string
		var enabled bool
		if err = rows.Scan(&service, &name, &enabled, &release); err != nil {
			rows.Close()
			return nil, 0, err
		}
		item, ok := services[service]
		if ok {
			item.Service.Environments[name] = WorkerEnvironment{Name: name, Enabled: enabled, Release: release}
			services[service] = item
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, 0, err
	}
	rows.Close()
	if err = tx.Commit(ctx); err != nil {
		return nil, 0, err
	}
	return services, generation, nil
}

// decodePublishedWorkerConfig validates reconstructed normalized state before
// it becomes the publisher's diff baseline. Returning an error is fail-closed:
// publishing a diff from nil could leave deleted routes alive on runtimes.
func decodePublishedWorkerConfig(raw []byte) (workerConfigProjection, error) {
	var projection workerConfigProjection
	if len(raw) == 0 {
		return projection, errors.New("published worker config is empty")
	}
	if err := json.Unmarshal(raw, &projection); err != nil {
		return projection, err
	}
	if projection.Routes == nil || projection.Releases == nil {
		return projection, errors.New("published worker config is incomplete")
	}
	return projection, nil
}
