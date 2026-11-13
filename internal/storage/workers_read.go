package storage

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
	"github.com/krun-dev/krun/internal/domain"
)

// loadWorkers fills the public Workers metadata in one round trip. Build
// source/log blobs and repository access tokens are intentionally fetched by
// key only at the endpoints that need them.
func (s *Store) loadWorkers(ctx context.Context, st *domain.State) error {
	if st.Workers == nil {
		st.Workers = domain.NewWorkerState()
	}
	scope, ok := ctx.Value(ReadScopeKey{}).(ReadScope)
	if ok && scope.WorkerActivityOnly {
		return s.loadWorkerActivity(ctx, st, scope.Organization, scope.Worker)
	}
	batch := &pgx.Batch{}
	queueStateRead(ctx, batch, "krun_worker_buckets", `SELECT bucket_id, org_id, name, store_id, enabled, revision, created_at FROM krun_worker_buckets`)
	queueStateRead(ctx, batch, "krun_workers", `SELECT worker_id, org_id, network_profile, repository, repository_revision, domains_revision, preview_revision FROM krun_workers`)
	queueStateRead(ctx, batch, "krun_worker_placements", `SELECT worker_id, pool_id, org_id, network_profile, role, state, revision, created_at, updated_at FROM krun_worker_placements`)
	queueStateRead(ctx, batch, "krun_worker_domains", `SELECT worker_id, domain_id FROM krun_worker_domains ORDER BY worker_id, ordinal`)
	queueStateRead(ctx, batch, "krun_worker_environments", `SELECT worker_id, env_name, enabled, config_revision, variables, deployment_revision, version_id, confirmed_version_id, version_metadata, assets_binding, hyperdrive_bindings, candidate_version_id, candidate_weight, service_bindings, gradual_asset_retention_seconds FROM krun_worker_environments`)
	queueStateRead(ctx, batch, "krun_worker_secret_versions", `SELECT secret_version_id, org_id, worker_id, env_name, name, value_digest, key_version, created_by, created_at FROM krun_worker_secret_versions`)
	queueStateRead(ctx, batch, "krun_worker_environment_secrets", `SELECT worker_id, env_name, name, secret_version_id FROM krun_worker_environment_secrets`)
	queueStateRead(ctx, batch, "krun_worker_builds", `SELECT `+workerBuildColumns+` FROM krun_worker_builds`)
	queueStateRead(ctx, batch, "krun_worker_artifacts", `SELECT `+workerArtifactColumns+` FROM krun_worker_artifacts`)
	queueStateRead(ctx, batch, "krun_worker_versions", `SELECT `+workerVersionColumns+` FROM krun_worker_versions`)
	queueStateRead(ctx, batch, "krun_worker_version_secrets", `SELECT version_id, name, secret_version_id FROM krun_worker_version_secrets`)
	queueStateRead(ctx, batch, "krun_worker_deployments", `SELECT deployment_id,org_id,worker_id,env_name,version_id,operation_id,created_at,expires_at,committed_at,candidate_version_id,candidate_weight,message FROM krun_worker_deployments`)
	queueStateRead(ctx, batch, "krun_worker_operations", `SELECT `+workerOperationColumns+` FROM krun_worker_operations`)
	queueStateRead(ctx, batch, "krun_worker_history", `SELECT worker_id, env_name, revision, version_id, operation_id, kind, created_at FROM krun_worker_history ORDER BY created_at DESC`)
	queueStateRead(ctx, batch, "krun_worker_idempotency", `SELECT idem_key, digest, target_id FROM krun_worker_idempotency`)
	queueStateRead(ctx, batch, "krun_worker_environment_r2", `SELECT worker_id, env_name, name, bucket_id FROM krun_worker_environment_r2`)
	queueStateRead(ctx, batch, "krun_worker_version_r2", `SELECT version_id, name, bucket_id FROM krun_worker_version_r2`)

	queueStateRead(ctx, batch, "krun_worker_configurations", `SELECT configuration_id,org_id,worker_id,env_name,name,message,variables,version_metadata,assets_binding,created_at,hyperdrive_bindings FROM krun_worker_configurations`)
	queueStateRead(ctx, batch, "krun_worker_configuration_secrets", `SELECT configuration_id,name,secret_version_id FROM krun_worker_configuration_secrets`)
	queueStateRead(ctx, batch, "krun_worker_configuration_r2", `SELECT configuration_id,name,bucket_id FROM krun_worker_configuration_r2`)

	results := s.pool.SendBatch(ctx, batch)
	defer results.Close()

	for _, read := range []func(pgx.BatchResults, *domain.State) error{
		readWorkerBuckets,
		readWorkers, readWorkerPlacements, readWorkerDomains, readWorkerEnvironments,
		readWorkerSecretVersions, readWorkerEnvironmentSecrets,
		readWorkerBuilds, readWorkerArtifacts,
		readWorkerVersions, readWorkerVersionSecrets, readWorkerDeployments, readWorkerOperations, readWorkerHistory,
		readWorkerIdempotency,
		readWorkerEnvironmentR2, readWorkerVersionR2,
		readWorkerConfigurations, readWorkerConfigurationSecrets, readWorkerConfigurationR2,
	} {
		if err := read(results, st); err != nil {
			return err
		}
	}
	return nil
}

func readWorkers(results pgx.BatchResults, st *domain.State) error {
	ws := st.Workers
	rows, err := results.Query()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, org, profile string
		var repository []byte
		var repositoryRevision, domainsRevision, previewRevision int
		if err = rows.Scan(&id, &org, &profile, &repository, &repositoryRevision, &domainsRevision, &previewRevision); err != nil {
			return err
		}
		svc := domain.Worker{
			Worker: id, Organization: org, NetworkProfile: profile, RepositoryRevision: repositoryRevision,
			DomainsRevision: domainsRevision, PreviewRevision: previewRevision,
			Domains: []string{}, Environments: map[string]domain.WorkerEnvironment{},
		}
		if len(repository) > 0 {
			var src domain.WorkerRepositorySource
			if err = json.Unmarshal(repository, &src); err != nil {
				return err
			}
			svc.Repository = &src
		}
		ws.Workers[id] = svc
	}
	return rows.Err()
}

func readWorkerPlacements(results pgx.BatchResults, st *domain.State) error {
	rows, err := results.Query()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var placement domain.WorkerPlacement
		if err = rows.Scan(&placement.Worker, &placement.Pool, &placement.Organization, &placement.NetworkProfile,
			&placement.Role, &placement.State, &placement.Revision, &placement.Created, &placement.Updated); err != nil {
			return err
		}
		st.Workers.Placements[domain.WorkerPlacementKey(placement.Worker, placement.Pool)] = placement
	}
	return rows.Err()
}

func readWorkerDomains(results pgx.BatchResults, st *domain.State) error {
	ws := st.Workers
	rows, err := results.Query()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var worker, domain string
		if err = rows.Scan(&worker, &domain); err != nil {
			return err
		}
		svc := ws.Workers[worker]
		svc.Domains = append(svc.Domains, domain)
		ws.Workers[worker] = svc
	}
	return rows.Err()
}

func readWorkerEnvironments(results pgx.BatchResults, st *domain.State) error {
	ws := st.Workers
	rows, err := results.Query()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var worker, name string
		var env domain.WorkerEnvironment
		var variables []byte
		if err = rows.Scan(&worker, &name, &env.Enabled, &env.ConfigRevision, &variables, &env.Revision, &env.Version, &env.Confirmed, &env.VersionMetadata, &env.AssetsBinding, &env.HyperdriveBindings, &env.Candidate, &env.CandidateWeight, &env.ServiceBindings, &env.AssetRetentionSeconds); err != nil {
			return err
		}
		env.Name = name
		env.Variables = map[string]any{}
		env.SecretVersions = map[string]string{}
		if len(variables) > 0 {
			if err = json.Unmarshal(variables, &env.Variables); err != nil {
				return err
			}
		}
		svc := ws.Workers[worker]
		if svc.Environments == nil {
			svc.Environments = map[string]domain.WorkerEnvironment{}
		}
		if name == domain.PreviewBaseName {
			svc.PreviewBase = &env
		} else {
			svc.Environments[name] = env
		}
		ws.Workers[worker] = svc
	}
	return rows.Err()
}

func readWorkerSecretVersions(results pgx.BatchResults, st *domain.State) error {
	rows, err := results.Query()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var version domain.WorkerSecretVersion
		if err = rows.Scan(&version.ID, &version.Organization, &version.Worker, &version.Environment,
			&version.Name, &version.ValueDigest, &version.KeyVersion, &version.CreatedBy, &version.Created); err != nil {
			return err
		}
		st.Workers.SecretVersions[version.ID] = version
	}
	return rows.Err()
}

func readWorkerEnvironmentSecrets(results pgx.BatchResults, st *domain.State) error {
	rows, err := results.Query()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var worker, environment, name, version string
		if err = rows.Scan(&worker, &environment, &name, &version); err != nil {
			return err
		}
		item, ok := st.Workers.Workers[worker]
		if !ok {
			continue
		}
		env, ok := item.Environments[environment]
		if environment == domain.PreviewBaseName && item.PreviewBase != nil {
			env = *item.PreviewBase
			ok = true
		}
		if !ok {
			continue
		}
		if env.SecretVersions == nil {
			env.SecretVersions = map[string]string{}
		}
		env.SecretVersions[name] = version
		env.SecretNames = domain.WorkerSecretNames(env.SecretVersions)
		if environment == domain.PreviewBaseName {
			item.PreviewBase = &env
		} else {
			item.Environments[environment] = env
		}
		st.Workers.Workers[worker] = item
	}
	return rows.Err()
}

func readWorkerBuilds(results pgx.BatchResults, st *domain.State) error {
	ws := st.Workers
	rows, err := results.Query()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		build, e := scanWorkerBuild(rows)
		if e != nil {
			return e
		}
		ws.Builds[build.ID] = build
	}
	return rows.Err()
}

func readWorkerArtifacts(results pgx.BatchResults, st *domain.State) error {
	ws := st.Workers
	rows, err := results.Query()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		artifact, e := scanWorkerArtifact(rows)
		if e != nil {
			return e
		}
		ws.Artifacts[artifact.ID] = artifact
	}
	return rows.Err()
}

func readWorkerVersions(results pgx.BatchResults, st *domain.State) error {
	ws := st.Workers
	rows, err := results.Query()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		version, e := scanWorkerVersion(rows)
		if e != nil {
			return e
		}
		ws.Versions[version.ID] = version
	}
	return rows.Err()
}

func readWorkerVersionSecrets(results pgx.BatchResults, st *domain.State) error {
	rows, err := results.Query()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var version, name, secretVersion string
		if err = rows.Scan(&version, &name, &secretVersion); err != nil {
			return err
		}
		item, ok := st.Workers.Versions[version]
		if !ok {
			continue
		}
		if item.SecretVersions == nil {
			item.SecretVersions = map[string]string{}
		}
		item.SecretVersions[name] = secretVersion
		st.Workers.Versions[version] = item
	}
	return rows.Err()
}

func readWorkerOperations(results pgx.BatchResults, st *domain.State) error {
	ws := st.Workers
	rows, err := results.Query()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		operation, actor, e := scanWorkerOperation(rows)
		if e != nil {
			return e
		}
		ws.Operations[operation.ID] = operation
		ws.OperationPrivate[operation.ID] = domain.WorkerOperationPrivate{Actor: actor}
	}
	return rows.Err()
}

func readWorkerHistory(results pgx.BatchResults, st *domain.State) error {
	ws := st.Workers
	rows, err := results.Query()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var worker, env string
		var h domain.WorkerHistory
		if err = rows.Scan(&worker, &env, &h.Revision, &h.Version, &h.Operation, &h.Kind, &h.Time); err != nil {
			return err
		}
		key := worker + "/" + env
		ws.Histories[key] = append(ws.Histories[key], h)
	}
	return rows.Err()
}

func readWorkerIdempotency(results pgx.BatchResults, st *domain.State) error {
	ws := st.Workers
	rows, err := results.Query()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var record domain.WorkerIdempotency
		if err = rows.Scan(&key, &record.Digest, &record.ID); err != nil {
			return err
		}
		ws.Idempotency[key] = record
	}
	return rows.Err()
}
