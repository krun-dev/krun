package storage

import (
	"context"
	"encoding/json"
	"errors"

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
	batch := &pgx.Batch{}
	queueStateRead(ctx, batch, `SELECT service_id, org_id, network_profile, repository, repository_revision, domains_revision, preview_revision FROM krun_worker_services`)
	queueStateRead(ctx, batch, `SELECT service_id, pool_id, org_id, network_profile, role, state, revision, created_at, updated_at FROM krun_worker_service_placements`)
	queueStateRead(ctx, batch, `SELECT service_id, domain_id FROM krun_worker_service_domains ORDER BY service_id, ordinal`)
	queueStateRead(ctx, batch, `SELECT service_id, env_name, enabled, config_revision, variables, deployment_revision, version_id, confirmed_version_id FROM krun_worker_environments`)
	queueStateRead(ctx, batch, `SELECT secret_version_id, org_id, service_id, env_name, name, value_digest, key_version, created_by, created_at FROM krun_worker_secret_versions`)
	queueStateRead(ctx, batch, `SELECT service_id, env_name, name, secret_version_id FROM krun_worker_environment_secrets`)
	queueStateRead(ctx, batch, `SELECT build_id, org_id, service_id, env_name, state, entrypoint, source_digest, profile, compatibility_date, compatibility_flags, job, artifact_id, error, repository, assets, created_at, finished_at, claimed_by, claimed_at FROM krun_worker_builds`)
	queueStateRead(ctx, batch, `SELECT artifact_id, org_id, state, profile, archive_digest, manifest_digest, module_digest, compatibility_date, compatibility_flags, size_bytes, object_key, storage_location, bucket, created_at FROM krun_worker_artifacts`)
	queueStateRead(ctx, batch, `SELECT version_id, org_id, service_id, env_name, artifact_id, profile, archive_digest, module_digest, compatibility_date, compatibility_flags, config_revision, variables, secret_digest, created_at FROM krun_worker_versions`)
	queueStateRead(ctx, batch, `SELECT version_id, name, secret_version_id FROM krun_worker_version_secrets`)
	queueStateRead(ctx, batch, `SELECT deployment_id,org_id,service_id,env_name,version_id,operation_id,created_at,expires_at,committed_at FROM krun_worker_deployments`)
	queueStateRead(ctx, batch, `SELECT operation_id, kind, org_id, service_id, env_name, state, build_id, version_id, expected_revision, config_revision, edge_revision, deployment_revision, error, steps, actor_user_id, actor_key_id, actor_auth_version, created_at, finished_at FROM krun_worker_operations`)
	queueStateRead(ctx, batch, `SELECT service_id, env_name, revision, version_id, operation_id, kind, created_at FROM krun_worker_history ORDER BY created_at DESC`)
	queueStateRead(ctx, batch, `SELECT idem_key, digest, target_id FROM krun_worker_idempotency`)

	results := s.pool.SendBatch(ctx, batch)
	defer results.Close()

	for _, read := range []func(pgx.BatchResults, *domain.State) error{
		readWorkerServices, readWorkerPlacements, readWorkerDomains, readWorkerEnvironments,
		readWorkerSecretVersions, readWorkerEnvironmentSecrets,
		readWorkerBuilds, readWorkerArtifacts,
		readWorkerVersions, readWorkerVersionSecrets, readWorkerDeployments, readWorkerOperations, readWorkerHistory,
		readWorkerIdempotency,
	} {
		if err := read(results, st); err != nil {
			return err
		}
	}
	return nil
}

func readWorkerServices(results pgx.BatchResults, st *domain.State) error {
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
		svc := domain.WorkerService{
			Service: id, Organization: org, NetworkProfile: profile, RepositoryRevision: repositoryRevision,
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
		ws.Services[id] = svc
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
		if err = rows.Scan(&placement.Service, &placement.Pool, &placement.Organization, &placement.NetworkProfile,
			&placement.Role, &placement.State, &placement.Revision, &placement.Created, &placement.Updated); err != nil {
			return err
		}
		st.Workers.Placements[domain.WorkerPlacementKey(placement.Service, placement.Pool)] = placement
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
		var service, domain string
		if err = rows.Scan(&service, &domain); err != nil {
			return err
		}
		svc := ws.Services[service]
		svc.Domains = append(svc.Domains, domain)
		ws.Services[service] = svc
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
		var service, name string
		var env domain.WorkerEnvironment
		var variables []byte
		if err = rows.Scan(&service, &name, &env.Enabled, &env.ConfigRevision, &variables, &env.Revision, &env.Version, &env.Confirmed); err != nil {
			return err
		}
		env.Name = name
		env.Variables = map[string]string{}
		env.SecretVersions = map[string]string{}
		if len(variables) > 0 {
			if err = json.Unmarshal(variables, &env.Variables); err != nil {
				return err
			}
		}
		svc := ws.Services[service]
		if svc.Environments == nil {
			svc.Environments = map[string]domain.WorkerEnvironment{}
		}
		if name == domain.PreviewBaseName {
			svc.PreviewBase = &env
		} else {
			svc.Environments[name] = env
		}
		ws.Services[service] = svc
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
		if err = rows.Scan(&version.ID, &version.Organization, &version.Service, &version.Environment,
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
		var service, environment, name, version string
		if err = rows.Scan(&service, &environment, &name, &version); err != nil {
			return err
		}
		item, ok := st.Workers.Services[service]
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
		st.Workers.Services[service] = item
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
		var b domain.WorkerBuild
		var repository, assets, flags []byte
		if err = rows.Scan(&b.ID, &b.Organization, &b.Service, &b.Environment, &b.State, &b.Entrypoint, &b.SourceDigest, &b.Profile, &b.CompatibilityDate, &flags, &b.Job, &b.Artifact, &b.Error, &repository, &assets, &b.Created, &b.Finished, &b.ClaimedBy, &b.ClaimedAt); err != nil {
			return err
		}
		if err = json.Unmarshal(flags, &b.CompatibilityFlags); err != nil {
			return err
		}
		if len(repository) > 0 {
			var src domain.WorkerBuildRepository
			if err = json.Unmarshal(repository, &src); err != nil {
				return err
			}
			b.Repository = &src
		}
		if len(assets) > 0 {
			var a domain.WorkerAssetsConfig
			if err = json.Unmarshal(assets, &a); err != nil {
				return err
			}
			b.Assets = &a
		}
		ws.Builds[b.ID] = b
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
		var a domain.WorkerArtifact
		var flags []byte
		if err = rows.Scan(&a.ID, &a.Organization, &a.State, &a.Profile, &a.ArchiveDigest, &a.ManifestDigest, &a.ModuleDigest, &a.CompatibilityDate, &flags, &a.Size, &a.ObjectKey, &a.StorageLocation, &a.Bucket, &a.Created); err != nil {
			return err
		}
		if err = json.Unmarshal(flags, &a.CompatibilityFlags); err != nil {
			return err
		}
		ws.Artifacts[a.ID] = a
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
		var r domain.WorkerVersion
		var variables, flags []byte
		if err = rows.Scan(&r.ID, &r.Organization, &r.Service, &r.Environment, &r.Artifact, &r.Profile, &r.ArchiveDigest, &r.ModuleDigest, &r.CompatibilityDate, &flags, &r.ConfigRevision, &variables, &r.SecretDigest, &r.Created); err != nil {
			return err
		}
		if err = json.Unmarshal(flags, &r.CompatibilityFlags); err != nil {
			return err
		}
		r.Variables = map[string]string{}
		r.SecretVersions = map[string]string{}
		if len(variables) > 0 {
			if err = json.Unmarshal(variables, &r.Variables); err != nil {
				return err
			}
		}
		ws.Versions[r.ID] = r
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
		var op domain.WorkerOperation
		var steps []byte
		var actor domain.WorkerActor
		if err = rows.Scan(&op.ID, &op.Kind, &op.Organization, &op.Service, &op.Environment, &op.State, &op.Build, &op.Version, &op.ExpectedRevision, &op.ConfigRevision, &op.EdgeRevision, &op.DeploymentRevision, &op.Error, &steps, &actor.User, &actor.Key, &actor.AuthVersion, &op.Created, &op.Finished); err != nil {
			return err
		}
		if len(steps) > 0 {
			if err = json.Unmarshal(steps, &op.Steps); err != nil {
				return err
			}
		}
		ws.Operations[op.ID] = op
		ws.OperationPrivate[op.ID] = domain.WorkerOperationPrivate{Actor: actor}
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
		var service, env string
		var h domain.WorkerHistory
		if err = rows.Scan(&service, &env, &h.Revision, &h.Version, &h.Operation, &h.Kind, &h.Time); err != nil {
			return err
		}
		key := service + "/" + env
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

var errNoState = errors.New("state is not initialized")
