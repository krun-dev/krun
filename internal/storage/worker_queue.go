package storage

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/krun-dev/krun/internal/domain"
)

// pendingWork returns the unfinished operations and builds for one reconcile
// cycle. Both queries use partial indexes, so an idle control plane scans no
// rows instead of deserializing a whole document.
func (s *Store) PendingWork(ctx context.Context, limit int) (domain.WorkerState, error) {
	replica, _ := ctx.Value(WorkerQueueReplicaKey{}).(string)

	state := domain.WorkerState{
		Builds:           map[string]domain.WorkerBuild{},
		BuildPrivate:     map[string]domain.WorkerBuildPrivate{},
		Artifacts:        map[string]domain.WorkerArtifact{},
		Versions:         map[string]domain.WorkerVersion{},
		Operations:       map[string]domain.WorkerOperation{},
		OperationPrivate: map[string]domain.WorkerOperationPrivate{},
		Histories:        map[string][]domain.WorkerHistory{},
		Idempotency:      map[string]domain.WorkerIdempotency{},
		Workers:          map[string]domain.Worker{},
		Placements:       map[string]domain.WorkerPlacement{},
		SecretVersions:   map[string]domain.WorkerSecretVersion{},
	}

	rows, err := s.pool.Query(ctx, `SELECT `+workerOperationColumns+`
		FROM krun_worker_operations
		WHERE state NOT IN ('succeeded','failed','available','superseded') AND reconcile_after<=now() AND (state IN ('queued','committed') OR claimed_by=$2 OR claimed_at IS NULL OR claimed_at<now()-'60 seconds'::interval)
		ORDER BY reconcile_after,created_at LIMIT $1`, limit, replica)
	if err != nil {
		return state, err
	}
	ops, err := scanOperations(rows)
	rows.Close()
	if err != nil {
		return state, err
	}
	for _, op := range ops {
		state.Operations[op.Operation.ID] = op.Operation
		state.OperationPrivate[op.Operation.ID] = domain.WorkerOperationPrivate{Actor: op.Actor}
	}
	rows, err = s.pool.Query(ctx, `SELECT `+workerBuildColumns+`
		FROM krun_worker_builds
		WHERE state NOT IN ('succeeded','failed') AND reconcile_after<=now() AND (state='queued' OR claimed_by=$2 OR claimed_at IS NULL OR claimed_at<now()-'60 seconds'::interval)
		ORDER BY reconcile_after,created_at LIMIT $1`, limit, replica)
	if err != nil {
		return state, err
	}
	builds, err := scanBuilds(rows)
	rows.Close()
	if err != nil {
		return state, err
	}
	for _, b := range builds {
		state.Builds[b.ID] = b
	}
	if len(state.Builds) > 0 {
		// The build actor and its source live in the private table; without them
		// re-authorization sees an empty actor and rejects every build.
		ids := make([]string, 0, len(state.Builds))
		for id := range state.Builds {
			ids = append(ids, id)
		}
		rows, err = s.pool.Query(ctx, `SELECT build_id, actor_user_id, actor_key_id, actor_auth_version, source_key, config_path, files, log_key
			FROM krun_worker_build_private WHERE build_id = ANY($1)`, ids)
		if err != nil {
			return state, err
		}
		if err = readWorkerBuildPrivateRows(rows, &state); err != nil {
			rows.Close()
			return state, err
		}
		rows.Close()
	}
	if len(state.Operations) > 0 {
		// processOperation needs the version and artifact it is validating.
		versionIDs := []string{}
		for _, op := range state.Operations {
			if op.Version != "" {
				versionIDs = append(versionIDs, op.Version)
			}
		}
		if len(versionIDs) > 0 {
			rows, err = s.pool.Query(ctx, `SELECT `+workerVersionColumns+`
				FROM krun_worker_versions WHERE version_id = ANY($1)`, versionIDs)
			if err != nil {
				return state, err
			}
			if err = readVersionRows(rows, &state); err != nil {
				rows.Close()
				return state, err
			}
			rows.Close()
			if err := loadVersionStorage(ctx, s.pool, state.Versions); err != nil {
				return state, err
			}
			workerIDs := []string{}
			for _, version := range state.Versions {
				workerIDs = append(workerIDs, version.Worker)
			}
			if len(workerIDs) > 0 {
				rows, err = s.pool.Query(ctx, `SELECT worker_id, pool_id, org_id, network_profile, role, state, revision, created_at, updated_at
					FROM krun_worker_placements WHERE worker_id = ANY($1)`, workerIDs)
				if err != nil {
					return state, err
				}
				for rows.Next() {
					var placement domain.WorkerPlacement
					if err = rows.Scan(&placement.Worker, &placement.Pool, &placement.Organization, &placement.NetworkProfile,
						&placement.Role, &placement.State, &placement.Revision, &placement.Created, &placement.Updated); err != nil {
						rows.Close()
						return state, err
					}
					state.Placements[domain.WorkerPlacementKey(placement.Worker, placement.Pool)] = placement
				}
				if err = rows.Err(); err != nil {
					rows.Close()
					return state, err
				}
				rows.Close()
			}
		}
		artifactIDs := []string{}
		for _, version := range state.Versions {
			if version.Artifact != "" {
				artifactIDs = append(artifactIDs, version.Artifact)
			}
		}
		if len(artifactIDs) > 0 {
			rows, err = s.pool.Query(ctx, `SELECT `+workerArtifactColumns+`
				FROM krun_worker_artifacts WHERE artifact_id = ANY($1)`, artifactIDs)
			if err != nil {
				return state, err
			}
			if err = readArtifactRows(rows, &state); err != nil {
				rows.Close()
				return state, err
			}
			rows.Close()
		}
	}
	return state, nil
}

func readWorkerBuildPrivateRows(rows pgx.Rows, state *domain.WorkerState) error {
	for rows.Next() {
		var id string
		var p domain.WorkerBuildPrivate
		var files []byte
		if err := rows.Scan(&id, &p.Actor.User, &p.Actor.Key, &p.Actor.AuthVersion, &p.SourceKey, &p.ConfigPath, &files, &p.LogKey); err != nil {
			return err
		}
		if len(files) > 0 {
			if err := json.Unmarshal(files, &p.Files); err != nil {
				return err
			}
		}
		state.BuildPrivate[id] = p
	}
	return rows.Err()
}

func readVersionRows(rows pgx.Rows, state *domain.WorkerState) error {
	for rows.Next() {
		version, err := scanWorkerVersion(rows)
		if err != nil {
			return err
		}
		state.Versions[version.ID] = version
	}
	return rows.Err()
}

func readArtifactRows(rows pgx.Rows, state *domain.WorkerState) error {
	for rows.Next() {
		artifact, err := scanWorkerArtifact(rows)
		if err != nil {
			return err
		}
		state.Artifacts[artifact.ID] = artifact
	}
	return rows.Err()
}

func scanOperations(rows pgx.Rows) ([]struct {
	Operation domain.WorkerOperation
	Actor     domain.WorkerActor
}, error) {
	out := []struct {
		Operation domain.WorkerOperation
		Actor     domain.WorkerActor
	}{}
	for rows.Next() {
		operation, actor, err := scanWorkerOperation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, struct {
			Operation domain.WorkerOperation
			Actor     domain.WorkerActor
		}{Operation: operation, Actor: actor})
	}
	return out, rows.Err()
}

func scanBuilds(rows pgx.Rows) ([]domain.WorkerBuild, error) {
	out := []domain.WorkerBuild{}
	for rows.Next() {
		build, err := scanWorkerBuild(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, build)
	}
	return out, rows.Err()
}

type WorkerQueueReplicaKey struct{}

// Rescheduling rotates unfinished tasks behind new arrivals instead of keeping
// the oldest 100 as a global admission window shared by every replica.
func (s *Store) ScheduleWorkerRetry(ctx context.Context, kind, id string) error {
	table, column := "krun_worker_builds", "build_id"
	if kind == "operation" {
		table, column = "krun_worker_operations", "operation_id"
	}
	_, err := s.pool.Exec(ctx, `UPDATE `+table+` SET reconcile_after=now()+'2 seconds'::interval WHERE `+column+`=$1`, id)
	return err
}

func (s *Store) WorkerExecutionStarted(ctx context.Context, kind, id string, fallback time.Time) (time.Time, error) {
	table, column := "krun_worker_builds", "build_id"
	if kind == "operation" {
		table, column = "krun_worker_operations", "operation_id"
	}
	var started *time.Time
	err := s.pool.QueryRow(ctx, `SELECT started_at FROM `+table+` WHERE `+column+`=$1`, id).Scan(&started)
	if err != nil {
		return fallback, err
	}
	if started == nil {
		return now(), nil
	}
	return *started, nil
}
