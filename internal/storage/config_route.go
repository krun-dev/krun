package storage

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
	"github.com/krun-dev/krun/internal/domain"
)

const edgeRouteColumns = "r.worker_id,r.host,r.pool,r.path,r.enabled,r.version_id,r.expires_at,r.candidate_version_id,r.candidate_weight,r.script_name,r.env_name,r.public,r.retained_versions"
const edgeRouteReferencesVersion = `c.version_id IN (r.version_id,r.candidate_version_id)
	OR r.retained_versions @> jsonb_build_array(jsonb_build_object('version_id', c.version_id))`

func scanEdgeRoute(row rowScanner) (domain.EdgeConfigRoute, error) {
	var r domain.EdgeConfigRoute
	err := row.Scan(&r.Worker, &r.Host, &r.Pool, &r.Path, &r.Enabled, &r.Version, &r.Expires,
		&r.Candidate, &r.CandidateWeight, &r.ScriptName, &r.Environment, &r.Public, &r.Retained)
	if len(r.Retained) == 0 {
		r.Retained = nil
	}
	return r, err
}

func readEdgeRouteVersions(ctx context.Context, tx pgx.Tx, pool string, routes []domain.EdgeConfigRoute, versions map[string]domain.WorkerVersion) error {
	ids := []string{}
	for _, route := range routes {
		ids = append(ids, route.VersionIDs()...)
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, "SELECT payload FROM worker_config_versions WHERE pool=$1 AND version_id=ANY($2)", pool, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		var version domain.WorkerVersion
		if err := rows.Scan(&raw); err != nil {
			return err
		}
		if err := json.Unmarshal(raw, &version); err != nil {
			return err
		}
		versions[version.ID] = version
	}
	return rows.Err()
}
