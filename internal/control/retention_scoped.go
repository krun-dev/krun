package control

import (
	"context"
	"strings"
)

// Simple age-indexed deletes are bounded even when a long outage left a large
// backlog. Progress commits every batch and is safe to resume next tick.
func boundedRetentionDelete(query string) string {
	if !strings.HasPrefix(query, "DELETE FROM ") {
		return query
	}
	parts := strings.SplitN(strings.TrimPrefix(query, "DELETE FROM "), " WHERE ", 2)
	if len(parts) != 2 || strings.Contains(parts[0], " ") {
		return query
	}
	return "DELETE FROM " + parts[0] + " WHERE ctid IN (SELECT ctid FROM " + parts[0] + " WHERE " + parts[1] + " LIMIT 1000)"
}
func (s *Store) retainWorkerScopes(ctx context.Context) error {
	rows, err := s.pool.Query(ctx, `SELECT service_id,generation FROM worker_retention_scopes ORDER BY generation LIMIT 256`)
	if err != nil {
		return err
	}
	type scope struct {
		id         string
		generation int64
	}
	scopes := []scope{}
	for rows.Next() {
		var v scope
		if err = rows.Scan(&v.id, &v.generation); err != nil {
			rows.Close()
			return err
		}
		scopes = append(scopes, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, v := range scopes {
		more := false
		// Each window is restricted to one dirty service, using the existing
		// (service,environment,created_at) and (service,created_at) indexes.
		tag, e := s.pool.Exec(ctx, `DELETE FROM krun_worker_history WHERE (service_id,env_name,revision) IN (
   SELECT service_id,env_name,revision FROM (SELECT service_id,env_name,revision,row_number() OVER(PARTITION BY env_name ORDER BY created_at DESC,revision DESC) n FROM krun_worker_history WHERE service_id=$1) ranked WHERE n>$2 LIMIT 1000)`, v.id, workerHistoryRetainPerEnv)
		if e != nil {
			return e
		}
		more = more || tag.RowsAffected() == 1000
		tag, e = s.pool.Exec(ctx, `WITH victims AS MATERIALIZED(SELECT build_id FROM krun_worker_builds WHERE service_id=$1 AND state IN('succeeded','failed') ORDER BY created_at DESC,build_id DESC OFFSET $2 LIMIT 1000), private AS(DELETE FROM krun_worker_build_private WHERE build_id IN(SELECT build_id FROM victims)) DELETE FROM krun_worker_builds WHERE build_id IN(SELECT build_id FROM victims)`, v.id, workerBuildRetainPerService)
		if e != nil {
			return e
		}
		more = more || tag.RowsAffected() == 1000
		// Deleted services are known from the resource deletion event. No repeated
		// full-table anti-join over every surviving service is necessary.
		var live bool
		if e = s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM krun_resources WHERE resource_id=$1 AND kind='service')`, v.id).Scan(&live); e != nil {
			return e
		}
		if !live {
			queries := []string{
				`WITH victims AS MATERIALIZED(SELECT build_id FROM krun_worker_builds WHERE service_id=$1 LIMIT 1000), private AS(DELETE FROM krun_worker_build_private WHERE build_id IN(SELECT build_id FROM victims)) DELETE FROM krun_worker_builds WHERE build_id IN(SELECT build_id FROM victims)`,
				`WITH victims AS MATERIALIZED(SELECT release_id FROM krun_worker_releases WHERE service_id=$1 LIMIT 1000), bindings AS(DELETE FROM krun_worker_release_secrets WHERE release_id IN(SELECT release_id FROM victims)) DELETE FROM krun_worker_releases WHERE release_id IN(SELECT release_id FROM victims)`,
			}
			for _, query := range queries {
				tag, e = s.pool.Exec(ctx, query, v.id)
				if e != nil {
					return e
				}
				more = more || tag.RowsAffected() == 1000
			}
			for _, table := range []string{"krun_worker_history", "krun_worker_operations", "krun_worker_environment_secrets", "krun_worker_environments", "krun_worker_service_domains", "krun_worker_service_placements", "krun_worker_services"} {
				tag, e = s.pool.Exec(ctx, `DELETE FROM `+table+` WHERE ctid IN(SELECT ctid FROM `+table+` WHERE service_id=$1 LIMIT 1000)`, v.id)
				if e != nil {
					return e
				}
				more = more || tag.RowsAffected() == 1000
			}
		}
		if !more {
			if _, e = s.pool.Exec(ctx, `DELETE FROM worker_retention_scopes WHERE service_id=$1 AND generation=$2`, v.id, v.generation); e != nil {
				return e
			}
		}
	}
	return nil
}

// A bounded keyset sweep catches pre-existing or externally-created orphan
// input rows. The cursor is durable; a restart resumes rather than rescans.
func (s *Store) retainOrphanInputs(ctx context.Context) error {
	const cursor = "retention_build_input_cursor"
	var after string
	err := s.pool.QueryRow(ctx, `WITH candidates AS MATERIALIZED(SELECT build_id FROM krun_worker_build_private WHERE build_id>COALESCE((SELECT value FROM krun_meta WHERE key=$1),'') ORDER BY build_id LIMIT 1000),removed AS(DELETE FROM krun_worker_build_private p WHERE build_id IN(SELECT build_id FROM candidates) AND NOT EXISTS(SELECT 1 FROM krun_worker_builds b WHERE b.build_id=p.build_id)) SELECT COALESCE(max(build_id),'') FROM candidates`, cursor).Scan(&after)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO krun_meta(key,value) VALUES($1,$2) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, cursor, after)
	return err
}
