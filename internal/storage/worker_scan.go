package storage

import (
	"encoding/json"

	"github.com/krun-dev/krun/internal/domain"
)

// One column list and one row decoder per mutable Worker entity. Batch reads,
// queue reads and projection reads all go through these so a new column cannot
// be added to one path and forgotten in another.
const (
	workerBuildColumns = "build_id, org_id, worker_id, env_name, state, entrypoint, source_digest, profile, compatibility_date, compatibility_flags, job, artifact_id, error, repository, assets, created_at, finished_at, claimed_by, claimed_at"
	workerArtifactColumns = "artifact_id, org_id, state, profile, archive_digest, manifest_digest, module_digest, compatibility_date, compatibility_flags, size_bytes, object_key, storage_location, bucket, created_at"
	workerVersionColumns = "version_id, org_id, worker_id, env_name, artifact_id, profile, archive_digest, module_digest, compatibility_date, compatibility_flags, config_revision, variables, secret_digest, created_at, version_metadata, assets_binding, tag, message, created_by, created_by_name, configuration_id, hyperdrive_bindings"
	workerOperationColumns = "operation_id, kind, org_id, worker_id, env_name, state, build_id, version_id, expected_revision, config_revision, edge_revision, deployment_revision, error, steps, actor_user_id, actor_key_id, actor_auth_version, created_at, finished_at"
)

type rowScanner interface {
	Scan(dest ...any) error
}

func scanWorkerBuild(row rowScanner) (domain.WorkerBuild, error) {
	var build domain.WorkerBuild
	var repository, assets, flags []byte
	if err := row.Scan(&build.ID, &build.Organization, &build.Worker, &build.Environment, &build.State, &build.Entrypoint, &build.SourceDigest, &build.Profile, &build.CompatibilityDate, &flags, &build.Job, &build.Artifact, &build.Error, &repository, &assets, &build.Created, &build.Finished, &build.ClaimedBy, &build.ClaimedAt); err != nil {
		return domain.WorkerBuild{}, err
	}
	if err := json.Unmarshal(flags, &build.CompatibilityFlags); err != nil {
		return domain.WorkerBuild{}, err
	}
	if len(repository) > 0 {
		var source domain.WorkerBuildRepository
		if err := json.Unmarshal(repository, &source); err != nil {
			return domain.WorkerBuild{}, err
		}
		build.Repository = &source
	}
	if len(assets) > 0 {
		var config domain.WorkerAssetsConfig
		if err := json.Unmarshal(assets, &config); err != nil {
			return domain.WorkerBuild{}, err
		}
		build.Assets = &config
	}
	return build, nil
}

func scanWorkerArtifact(row rowScanner) (domain.WorkerArtifact, error) {
	var artifact domain.WorkerArtifact
	var flags []byte
	if err := row.Scan(&artifact.ID, &artifact.Organization, &artifact.State, &artifact.Profile, &artifact.ArchiveDigest, &artifact.ManifestDigest, &artifact.ModuleDigest, &artifact.CompatibilityDate, &flags, &artifact.Size, &artifact.ObjectKey, &artifact.StorageLocation, &artifact.Bucket, &artifact.Created); err != nil {
		return domain.WorkerArtifact{}, err
	}
	if err := json.Unmarshal(flags, &artifact.CompatibilityFlags); err != nil {
		return domain.WorkerArtifact{}, err
	}
	return artifact, nil
}

func scanWorkerVersion(row rowScanner) (domain.WorkerVersion, error) {
	var version domain.WorkerVersion
	var variables, flags []byte
	if err := row.Scan(&version.ID, &version.Organization, &version.Worker, &version.Environment, &version.Artifact, &version.Profile, &version.ArchiveDigest, &version.ModuleDigest, &version.CompatibilityDate, &flags, &version.ConfigRevision, &variables, &version.SecretDigest, &version.Created, &version.VersionMetadata, &version.AssetsBinding, &version.Tag, &version.Message, &version.CreatedBy, &version.CreatedByName, &version.Configuration, &version.HyperdriveBindings); err != nil {
		return domain.WorkerVersion{}, err
	}
	if err := json.Unmarshal(flags, &version.CompatibilityFlags); err != nil {
		return domain.WorkerVersion{}, err
	}
	version.Variables = map[string]any{}
	version.SecretVersions = map[string]string{}
	if len(variables) > 0 {
		if err := json.Unmarshal(variables, &version.Variables); err != nil {
			return domain.WorkerVersion{}, err
		}
	}
	return version, nil
}

func scanWorkerOperation(row rowScanner) (domain.WorkerOperation, domain.WorkerActor, error) {
	var operation domain.WorkerOperation
	var actor domain.WorkerActor
	var steps []byte
	if err := row.Scan(&operation.ID, &operation.Kind, &operation.Organization, &operation.Worker, &operation.Environment, &operation.State, &operation.Build, &operation.Version, &operation.ExpectedRevision, &operation.ConfigRevision, &operation.EdgeRevision, &operation.DeploymentRevision, &operation.Error, &steps, &actor.User, &actor.Key, &actor.AuthVersion, &operation.Created, &operation.Finished); err != nil {
		return domain.WorkerOperation{}, domain.WorkerActor{}, err
	}
	if len(steps) > 0 {
		if err := json.Unmarshal(steps, &operation.Steps); err != nil {
			return domain.WorkerOperation{}, domain.WorkerActor{}, err
		}
	}
	return operation, actor, nil
}
