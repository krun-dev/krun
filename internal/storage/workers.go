package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"maps"
	"reflect"

	"github.com/jackc/pgx/v5"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/storage/models"
)

// persistWorkers writes only changed Workers rows. Operation and build state
// transitions use conditional updates so a concurrent claimer wins cleanly and
// this writer simply skips; that is the extent of the concurrency guarantee
// (AGENTS.md section 9: builds and versions converge eventually).
func (s *Store) persistWorkers(ctx context.Context, before, after *domain.State, audits []domain.Audit) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = s.writeWorkerBuckets(ctx, tx, before.Workers, after.Workers); err != nil {
		return err
	}
	if err = writeWorkersDiff(ctx, tx, before, after); err != nil {
		return err
	}
	if err = s.writeWorkerR2Bindings(ctx, tx, before.Workers, after.Workers); err != nil {
		return err
	}
	if err = s.writeWorkerConfigurations(ctx, tx, before, after); err != nil {
		return err
	}
	if err = insertAudits(ctx, tx, audits); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func writeWorkersDiff(ctx context.Context, tx pgx.Tx, before, after *domain.State) error {
	bw, aw := before.Workers, after.Workers
	if bw == nil {
		bw = domain.NewWorkerState()
	}
	if aw == nil {
		aw = domain.NewWorkerState()
	}
	// Reserve idempotency keys before inserting their build/operation rows.
	// The unique key is the cross-replica arbiter; a losing request rolls this
	// transaction back before it can create a second target.
	if err := writeIdempotency(ctx, tx, bw, aw); err != nil {
		return err
	}
	if err := writeWorkerSecretVersions(ctx, tx, bw, aw, after.PendingWorkerSecrets); err != nil {
		return err
	}
	if err := writeWorkers(ctx, tx, bw, aw); err != nil {
		return err
	}
	if err := writePlacements(ctx, tx, bw, aw); err != nil {
		return err
	}
	if err := writeBuilds(ctx, tx, bw, aw); err != nil {
		return err
	}
	if err := writeArtifacts(ctx, tx, bw, aw); err != nil {
		return err
	}
	if err := writeVersions(ctx, tx, bw, aw); err != nil {
		return err
	}
	if err := writeDeployments(ctx, tx, bw, aw); err != nil {
		return err
	}
	if err := writeOperations(ctx, tx, bw, aw); err != nil {
		return err
	}
	if err := writeHistories(ctx, tx, bw, aw); err != nil {
		return err
	}
	return nil
}

func writeWorkers(ctx context.Context, tx pgx.Tx, bw, aw *domain.WorkerState) error {
	for id, svc := range aw.Workers {
		old, existed := bw.Workers[id]
		repositoryChanged := !existed || old.RepositoryRevision != svc.RepositoryRevision || !reflect.DeepEqual(old.Repository, svc.Repository)
		domainsChanged := existed && old.DomainsRevision != svc.DomainsRevision
		profileChanged := existed && old.NetworkProfile != svc.NetworkProfile
		previewSetChanged := !existed && PreviewCount(svc.Environments) > 0 || existed && !samePreviewSet(old.Environments, svc.Environments)
		if repositoryChanged {
			var repository []byte
			if svc.Repository != nil {
				raw, err := json.Marshal(svc.Repository)
				if err != nil {
					return err
				}
				repository = raw
			}
			if !existed {
				tag, err := tx.Exec(ctx, `INSERT INTO krun_workers
					(worker_id, org_id, network_profile, repository, repository_revision, domains_revision, preview_revision)
					VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT (worker_id) DO NOTHING`, id, svc.Organization,
					svc.NetworkProfile, repository, svc.RepositoryRevision, svc.DomainsRevision, svc.PreviewRevision)
				if err != nil {
					return err
				}
				if tag.RowsAffected() == 1 {
					domainsChanged = true
				}
				// A default row may be materialised by two otherwise independent
				// requests (for example, the first two builds). That insert is
				// idempotent. A repository mutation, however, promises CAS semantics
				// for expected_revision=0 and only one writer may win.
				if tag.RowsAffected() == 0 && (svc.RepositoryRevision != 0 || svc.Repository != nil || svc.DomainsRevision != 0 || svc.PreviewRevision != 0) {
					return domain.Conflict("REVISION_CONFLICT", "Repository configuration changed")
				}
			} else {
				if svc.RepositoryRevision != old.RepositoryRevision+1 {
					return domain.Conflict("REVISION_CONFLICT", "Repository configuration changed")
				}
				tag, err := tx.Exec(ctx, `UPDATE krun_workers
					SET repository=$2, repository_revision=$3, updated_at=now()
					WHERE worker_id=$1 AND repository_revision=$4`, id, repository, svc.RepositoryRevision, old.RepositoryRevision)
				if err != nil {
					return err
				}
				if tag.RowsAffected() != 1 {
					return domain.Conflict("REVISION_CONFLICT", "Repository configuration changed")
				}
			}
		}
		if existed && old.DomainsRevision != svc.DomainsRevision {
			if svc.DomainsRevision != old.DomainsRevision+1 {
				return domain.Conflict("REVISION_CONFLICT", "Domain configuration changed")
			}
			tag, err := tx.Exec(ctx, `UPDATE krun_workers
				SET domains_revision=$2, network_profile=$4, updated_at=now()
				WHERE worker_id=$1 AND domains_revision=$3`, id, svc.DomainsRevision, old.DomainsRevision, svc.NetworkProfile)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return domain.Conflict("REVISION_CONFLICT", "Domain configuration changed")
			}
		}
		if profileChanged && !domainsChanged {
			tag, err := tx.Exec(ctx, `UPDATE krun_workers SET network_profile=$2, updated_at=now()
				WHERE worker_id=$1 AND network_profile=$3`, id, svc.NetworkProfile, old.NetworkProfile)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return domain.Conflict("REVISION_CONFLICT", "Runtime placement changed")
			}
		}
		if domainsChanged {
			if err := replaceWorkerDomains(ctx, tx, id, svc.Domains); err != nil {
				return err
			}
		}
		if existed && previewSetChanged {
			if svc.PreviewRevision != old.PreviewRevision+1 {
				return domain.Conflict("REVISION_CONFLICT", "Preview environments changed")
			}
			tag, err := tx.Exec(ctx, `UPDATE krun_workers
				SET preview_revision=$2, updated_at=now()
				WHERE worker_id=$1 AND preview_revision=$3`, id, svc.PreviewRevision, old.PreviewRevision)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return domain.Conflict("REVISION_CONFLICT", "Preview environments changed")
			}
		}
		for name, env := range domain.StorageWorkerEnvironments(svc) {
			oldEnv, ok := domain.StorageWorkerEnvironments(old)[name]
			if ok && sameEnvironment(oldEnv, env) {
				continue
			}
			variables, err := json.Marshal(env.Variables)
			if err != nil {
				return err
			}
			if !ok {
				tag, err := tx.Exec(ctx, `INSERT INTO krun_worker_environments
					(worker_id, env_name, enabled, config_revision, variables, deployment_revision, version_id, confirmed_version_id, version_metadata, assets_binding, hyperdrive_bindings, service_bindings, gradual_asset_retention_seconds)
					VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,COALESCE($11::jsonb,'{}'::jsonb),COALESCE(NULLIF($12::jsonb,'null'::jsonb),'{}'::jsonb),$13) ON CONFLICT (worker_id, env_name) DO NOTHING`,
					id, name, env.Enabled, env.ConfigRevision, variables, env.Revision, env.Version, env.Confirmed, env.VersionMetadata, env.AssetsBinding, env.HyperdriveBindings, env.ServiceBindings, env.AssetRetentionSeconds)
				if err != nil {
					return err
				}
				if tag.RowsAffected() == 0 && !defaultWorkerBaseline(name, env) {
					return domain.Conflict("REVISION_CONFLICT", "Environment configuration changed")
				}
			} else {
				tag, err := tx.Exec(ctx, `UPDATE krun_worker_environments
					SET enabled=$3, config_revision=$4, variables=$5, deployment_revision=$6,
					    version_id=$7, confirmed_version_id=$8, version_metadata=$13, assets_binding=$14, hyperdrive_bindings=COALESCE($15::jsonb,'{}'::jsonb), service_bindings=COALESCE(NULLIF($16::jsonb,'null'::jsonb),'{}'::jsonb), gradual_asset_retention_seconds=$17, updated_at=now()
					WHERE worker_id=$1 AND env_name=$2 AND config_revision=$9
					  AND deployment_revision=$10 AND version_id=$11 AND confirmed_version_id=$12`,
					id, name, env.Enabled, env.ConfigRevision, variables, env.Revision, env.Version, env.Confirmed,
					oldEnv.ConfigRevision, oldEnv.Revision, oldEnv.Version, oldEnv.Confirmed, env.VersionMetadata, env.AssetsBinding, env.HyperdriveBindings, env.ServiceBindings, env.AssetRetentionSeconds)
				if err != nil {
					return err
				}
				if tag.RowsAffected() != 1 {
					return domain.Conflict("REVISION_CONFLICT", "Environment configuration changed")
				}
				if oldEnv.Enabled && !env.Enabled {
					if err := supersedeEnvironmentOperations(ctx, tx, id, name, "Environment disabled"); err != nil {
						return err
					}
				}
			}
			if err := replaceWorkerEnvironmentSecrets(ctx, tx, id, name, env.SecretVersions); err != nil {
				return err
			}
		}
		for name, oldEnv := range domain.StorageWorkerEnvironments(old) {
			if _, ok := domain.StorageWorkerEnvironments(svc)[name]; !ok {
				if _, err := tx.Exec(ctx, `DELETE FROM krun_worker_environment_secrets WHERE worker_id=$1 AND env_name=$2`, id, name); err != nil {
					return err
				}
				tag, err := tx.Exec(ctx, `DELETE FROM krun_worker_environments
					WHERE worker_id=$1 AND env_name=$2 AND config_revision=$3
					  AND deployment_revision=$4 AND version_id=$5 AND confirmed_version_id=$6`,
					id, name, oldEnv.ConfigRevision, oldEnv.Revision, oldEnv.Version, oldEnv.Confirmed)
				if err != nil {
					return err
				}
				if tag.RowsAffected() != 1 {
					return domain.Conflict("REVISION_CONFLICT", "Environment configuration changed")
				}
				if err := supersedeEnvironmentOperations(ctx, tx, id, name, "Environment deleted"); err != nil {
					return err
				}
				if err := deleteEnvironmentConfigurations(ctx, tx, id, name); err != nil {
					return err
				}
			}
		}
	}
	for id := range bw.Workers {
		if _, ok := aw.Workers[id]; ok {
			continue
		}
		for _, statement := range []string{
			`DELETE FROM krun_worker_environment_secrets WHERE worker_id=$1`,
			`DELETE FROM krun_worker_domains WHERE worker_id=$1`,
			`DELETE FROM krun_worker_environments WHERE worker_id=$1`,
		} {
			if _, err := tx.Exec(ctx, statement, id); err != nil {
				return err
			}
		}
		if err := supersedeEnvironmentOperations(ctx, tx, id, "", "Worker deleted"); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM krun_workers WHERE worker_id=$1`, id); err != nil {
			return err
		}
	}
	return nil
}

func writePlacements(ctx context.Context, tx pgx.Tx, bw, aw *domain.WorkerState) error {
	// Remove superseded primaries before inserting their replacements so the
	// partial unique index remains the arbiter for one live primary.
	for key, placement := range bw.Placements {
		if _, ok := aw.Placements[key]; ok {
			continue
		}
		tag, err := tx.Exec(ctx, `DELETE FROM krun_worker_placements
			WHERE worker_id=$1 AND pool_id=$2 AND revision=$3`, placement.Worker, placement.Pool, placement.Revision)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return domain.Conflict("REVISION_CONFLICT", "Runtime placement changed")
		}
	}
	for key, placement := range aw.Placements {
		old, existed := bw.Placements[key]
		if existed && sameWorkerPlacement(old, placement) {
			continue
		}
		if !existed {
			_, err := tx.Exec(ctx, `INSERT INTO krun_worker_placements
				(worker_id, pool_id, org_id, network_profile, role, state, revision, created_at, updated_at)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, placement.Worker, placement.Pool,
				placement.Organization, placement.NetworkProfile, placement.Role, placement.State,
				placement.Revision, placement.Created, placement.Updated)
			if err != nil {
				return err
			}
			continue
		}
		tag, err := tx.Exec(ctx, `UPDATE krun_worker_placements
			SET org_id=$3, network_profile=$4, role=$5, state=$6, revision=$7, updated_at=$8
			WHERE worker_id=$1 AND pool_id=$2 AND revision=$9`, placement.Worker, placement.Pool,
			placement.Organization, placement.NetworkProfile, placement.Role, placement.State,
			placement.Revision, placement.Updated, old.Revision)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return domain.Conflict("REVISION_CONFLICT", "Runtime placement changed")
		}
	}
	return nil
}

func sameWorkerPlacement(a, b domain.WorkerPlacement) bool {
	return a.Worker == b.Worker && a.Organization == b.Organization &&
		a.NetworkProfile == b.NetworkProfile && a.Pool == b.Pool && a.Role == b.Role &&
		a.State == b.State && a.Revision == b.Revision && a.Created.Equal(b.Created) && a.Updated.Equal(b.Updated)
}

// The baseline row is lazily materialised. Two independent first requests may
// race to insert this exact value; the loser can safely continue because it did
// not intend to change environment configuration.
func defaultWorkerBaseline(name string, env domain.WorkerEnvironment) bool {
	return name == workerBaseline && env.Name == workerBaseline && env.Enabled &&
		env.ConfigRevision == 1 && env.Revision == 0 && env.Version == "" && env.Confirmed == "" &&
		len(env.Variables) == 0 && len(env.SecretVersions) == 0 && len(env.R2Bindings) == 0 && len(env.HyperdriveBindings) == 0 && len(env.ServiceBindings) == 0 && env.VersionMetadata == "" && env.AssetsBinding == ""
}

func PreviewCount(environments map[string]domain.WorkerEnvironment) int {
	count := 0
	for name := range environments {
		if name != workerBaseline {
			count++
		}
	}
	return count
}

func samePreviewSet(a, b map[string]domain.WorkerEnvironment) bool {
	if PreviewCount(a) != PreviewCount(b) {
		return false
	}
	for name := range a {
		if name == workerBaseline {
			continue
		}
		if _, ok := b[name]; !ok {
			return false
		}
	}
	return true
}

func replaceWorkerDomains(ctx context.Context, tx pgx.Tx, worker string, domains []string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM krun_worker_domains WHERE worker_id=$1`, worker); err != nil {
		return err
	}
	for i, domain := range domains {
		if _, err := tx.Exec(ctx, `INSERT INTO krun_worker_domains (worker_id, domain_id, ordinal) VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, worker, domain, i); err != nil {
			return err
		}
	}
	return nil
}

func sameEnvironment(a, b domain.WorkerEnvironment) bool {
	if !reflect.DeepEqual(a.AssetRetentionSeconds, b.AssetRetentionSeconds) {
		return false
	}
	if !reflect.DeepEqual(a.R2Bindings, b.R2Bindings) || !maps.Equal(a.HyperdriveBindings, b.HyperdriveBindings) || !sameServiceBindings(a.ServiceBindings, b.ServiceBindings) {
		return false
	}
	return a.VersionMetadata == b.VersionMetadata && a.AssetsBinding == b.AssetsBinding && a.Name == b.Name && a.Enabled == b.Enabled && a.ConfigRevision == b.ConfigRevision &&
		a.Revision == b.Revision && a.Version == b.Version && a.Confirmed == b.Confirmed &&
		reflect.DeepEqual(a.Variables, b.Variables) && reflect.DeepEqual(a.SecretVersions, b.SecretVersions)
}

func writeBuilds(ctx context.Context, tx pgx.Tx, bw, aw *domain.WorkerState) error {
	for id, b := range aw.Builds {
		old, existed := bw.Builds[id]
		if existed && sameBuild(old, b) {
			continue
		}
		var repository, assets []byte
		flags, err := json.Marshal(b.CompatibilityFlags)
		if err != nil {
			return err
		}
		if b.Repository != nil {
			raw, err := json.Marshal(b.Repository)
			if err != nil {
				return err
			}
			repository = raw
		}
		if b.Assets != nil {
			raw, err := json.Marshal(b.Assets)
			if err != nil {
				return err
			}
			assets = raw
		}
		if !existed {
			if !workerBuildTerminal(b.State) {
				tag, err := tx.Exec(ctx, `UPDATE krun_workers
					SET active_build_count=active_build_count+1, updated_at=now()
					WHERE worker_id=$1 AND active_build_count < $2`, b.Worker, WorkerActiveBuildLimit)
				if err != nil {
					return err
				}
				if tag.RowsAffected() != 1 {
					return domain.Conflict("BUILD_LIMIT", "At most 100 builds may be active per worker")
				}
			}
			if private, ok := aw.BuildPrivate[id]; ok && private.SourceKey != "" {
				if err := reserveWorkerSourceUpload(ctx, tx, private.SourceKey, b.Organization, id); err != nil {
					return err
				}
			}
			if _, err := tx.Exec(ctx, `INSERT INTO krun_worker_builds (build_id, org_id, worker_id, env_name, state, entrypoint, source_digest, profile, compatibility_date, compatibility_flags, job, artifact_id, error, repository, assets, created_at, finished_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`, id, b.Organization, b.Worker, b.Environment, b.State, b.Entrypoint, b.SourceDigest, b.Profile, b.CompatibilityDate, flags, b.Job, b.Artifact, b.Error, repository, assets, b.Created, b.Finished); err != nil {
				return err
			}
		} else {
			tag, err := tx.Exec(ctx, `UPDATE krun_worker_builds
				SET org_id=$2, worker_id=$3, env_name=$4, state=$5, entrypoint=$6,
				    source_digest=$7, profile=$8, compatibility_date=$9, compatibility_flags=$10,
				    job=$11, artifact_id=$12, error=$13, repository=$14, assets=$15,
				    created_at=$16, finished_at=$17
				WHERE build_id=$1 AND state=$18 AND claimed_by=$19
				  AND claimed_at IS NOT DISTINCT FROM $20`, id, b.Organization, b.Worker,
				b.Environment, b.State, b.Entrypoint, b.SourceDigest, b.Profile,
				b.CompatibilityDate, flags, b.Job, b.Artifact, b.Error, repository, assets, b.Created, b.Finished,
				old.State, old.ClaimedBy, old.ClaimedAt)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return errors.New("build changed while persisting snapshot")
			}
		}
	}
	for id := range bw.Builds {
		if _, ok := aw.Builds[id]; ok {
			continue
		}
		var deletedState, deletedWorker string
		old := bw.Builds[id]
		err := tx.QueryRow(ctx, `DELETE FROM krun_worker_builds
			WHERE build_id=$1 AND state=$2 AND claimed_by=$3
			  AND claimed_at IS NOT DISTINCT FROM $4
			RETURNING state, worker_id`, id, old.State, old.ClaimedBy, old.ClaimedAt).Scan(&deletedState, &deletedWorker)
		if errors.Is(err, pgx.ErrNoRows) {
			return errors.New("build changed while deleting snapshot")
		}
		if err != nil {
			return err
		}
		if !workerBuildTerminal(deletedState) {
			if _, err := tx.Exec(ctx, `UPDATE krun_workers
				SET active_build_count=GREATEST(active_build_count-1,0), updated_at=now()
				WHERE worker_id=$1`, deletedWorker); err != nil {
				return err
			}
		}
	}
	for id, p := range aw.BuildPrivate {
		old, existed := bw.BuildPrivate[id]
		if existed && sameBuildPrivate(old, p) {
			continue
		}
		var files []byte
		if p.Files != nil {
			raw, err := json.Marshal(p.Files)
			if err != nil {
				return err
			}
			files = raw
		}
		_, buildExisted := bw.Builds[id]
		if buildExisted && (!existed || old.SourceKey == "") && p.SourceKey != "" {
			build, ok := aw.Builds[id]
			if !ok {
				return errors.New("build source has no build metadata")
			}
			if err := registerGeneratedBuildSourceTx(ctx, tx, id, build.Organization, p.SourceKey); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO krun_worker_build_private (build_id, actor_user_id, actor_key_id, actor_auth_version, source_key, config_path, files, log_key)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
			ON CONFLICT (build_id) DO UPDATE SET actor_user_id=excluded.actor_user_id,
			actor_key_id=excluded.actor_key_id, actor_auth_version=excluded.actor_auth_version,
			source_key=excluded.source_key, config_path=excluded.config_path,
			files=excluded.files, log_key=excluded.log_key`, id, p.Actor.User, p.Actor.Key, p.Actor.AuthVersion, p.SourceKey, p.ConfigPath, files, p.LogKey); err != nil {
			return err
		}
	}
	for id := range bw.BuildPrivate {
		if _, ok := aw.BuildPrivate[id]; !ok {
			if _, err := tx.Exec(ctx, `DELETE FROM krun_worker_build_private WHERE build_id=$1`, id); err != nil {
				return err
			}
		}
	}
	return nil
}

const WorkerActiveBuildLimit = 100

func workerBuildTerminal(state string) bool {
	return state == "succeeded" || state == "failed"
}

func sameBuild(a, b domain.WorkerBuild) bool {
	if !reflect.DeepEqual(a.Repository, b.Repository) || !reflect.DeepEqual(a.Assets, b.Assets) {
		return false
	}
	if a.Finished == nil || b.Finished == nil {
		if (a.Finished == nil) != (b.Finished == nil) {
			return false
		}
	} else if !a.Finished.Equal(*b.Finished) {
		return false
	}
	return a.ID == b.ID && a.Organization == b.Organization && a.Worker == b.Worker &&
		a.Environment == b.Environment && a.State == b.State && a.Entrypoint == b.Entrypoint &&
		a.SourceDigest == b.SourceDigest && a.Profile == b.Profile &&
		a.CompatibilityDate == b.CompatibilityDate && sameStrings(a.CompatibilityFlags, b.CompatibilityFlags) && a.Job == b.Job &&
		a.Artifact == b.Artifact && a.Error == b.Error && a.Created.Equal(b.Created)
}

func sameBuildPrivate(a, b domain.WorkerBuildPrivate) bool {
	return a.Actor == b.Actor && a.SourceKey == b.SourceKey && a.ConfigPath == b.ConfigPath && reflect.DeepEqual(a.Files, b.Files) && a.LogKey == b.LogKey
}

// workerBuildPrivate fetches one potentially large source row and its log object key. List and
// metadata endpoints deliberately never materialize this table.
func (s *Store) WorkerBuildPrivate(ctx context.Context, id string) (domain.WorkerBuildPrivate, bool, error) {
	var row models.BuildPrivate
	err := s.orm.NewSelect().Model(&row).Where("build_id = ?", id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.WorkerBuildPrivate{}, false, nil
	}
	if err != nil {
		return domain.WorkerBuildPrivate{}, false, err
	}

	private := domain.WorkerBuildPrivate{
		Actor: domain.WorkerActor{
			User:        row.ActorUserID,
			Key:         row.ActorKeyID,
			AuthVersion: row.ActorAuthVersion,
		},
		SourceKey:  row.SourceKey,
		ConfigPath: row.ConfigPath,
		LogKey:     row.LogKey,
	}
	if len(row.Files) > 0 {
		if err = json.Unmarshal(row.Files, &private.Files); err != nil {
			return domain.WorkerBuildPrivate{}, false, err
		}
	}
	return private, true, nil
}

func writeArtifacts(ctx context.Context, tx pgx.Tx, bw, aw *domain.WorkerState) error {
	for id, a := range aw.Artifacts {
		if old, ok := bw.Artifacts[id]; ok && sameArtifact(old, a) {
			continue
		}
		flags, err := json.Marshal(a.CompatibilityFlags)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO krun_worker_artifacts (artifact_id, org_id, state, profile, archive_digest, manifest_digest, module_digest, compatibility_date, compatibility_flags, size_bytes, object_key, storage_location, bucket, created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14) ON CONFLICT (artifact_id) DO UPDATE SET state=excluded.state, profile=excluded.profile, archive_digest=excluded.archive_digest, manifest_digest=excluded.manifest_digest, module_digest=excluded.module_digest, compatibility_date=excluded.compatibility_date, compatibility_flags=excluded.compatibility_flags, size_bytes=excluded.size_bytes, object_key=excluded.object_key, storage_location=excluded.storage_location, bucket=excluded.bucket`, id, a.Organization, a.State, a.Profile, a.ArchiveDigest, a.ManifestDigest, a.ModuleDigest, a.CompatibilityDate, flags, a.Size, a.ObjectKey, a.StorageLocation, a.Bucket, a.Created); err != nil {
			return err
		}
	}
	for id := range bw.Artifacts {
		if _, ok := aw.Artifacts[id]; !ok {
			if _, err := tx.Exec(ctx, `DELETE FROM krun_worker_artifacts WHERE artifact_id=$1`, id); err != nil {
				return err
			}
		}
	}
	return nil
}

func sameArtifact(a, b domain.WorkerArtifact) bool {
	return a.ID == b.ID && a.Organization == b.Organization && a.State == b.State &&
		a.Profile == b.Profile && a.ArchiveDigest == b.ArchiveDigest &&
		a.ManifestDigest == b.ManifestDigest && a.ModuleDigest == b.ModuleDigest &&
		a.CompatibilityDate == b.CompatibilityDate && sameStrings(a.CompatibilityFlags, b.CompatibilityFlags) &&
		a.Size == b.Size && a.ObjectKey == b.ObjectKey && a.StorageLocation == b.StorageLocation &&
		a.Bucket == b.Bucket && a.Created.Equal(b.Created) && a.UseRevision == b.UseRevision
}

func sameVersion(a, b domain.WorkerVersion) bool {
	if !reflect.DeepEqual(a.R2Bindings, b.R2Bindings) || !maps.Equal(a.HyperdriveBindings, b.HyperdriveBindings) || !sameServiceBindings(a.ServiceBindings, b.ServiceBindings) {
		return false
	}
	return a.Configuration == b.Configuration && a.CreatedBy == b.CreatedBy && a.CreatedByName == b.CreatedByName && a.VersionMetadata == b.VersionMetadata && a.AssetsBinding == b.AssetsBinding && a.Tag == b.Tag && a.Message == b.Message && a.ID == b.ID && a.Worker == b.Worker && a.Organization == b.Organization &&
		a.Environment == b.Environment && a.Artifact == b.Artifact && a.Profile == b.Profile && a.ArchiveDigest == b.ArchiveDigest &&
		a.ModuleDigest == b.ModuleDigest && a.CompatibilityDate == b.CompatibilityDate &&
		sameStrings(a.CompatibilityFlags, b.CompatibilityFlags) && a.ConfigRevision == b.ConfigRevision &&
		reflect.DeepEqual(a.Variables, b.Variables) && a.SecretDigest == b.SecretDigest &&
		reflect.DeepEqual(a.SecretVersions, b.SecretVersions) && a.Created.Equal(b.Created)
}

func writeVersions(ctx context.Context, tx pgx.Tx, bw, aw *domain.WorkerState) error {
	for id, r := range aw.Versions {
		old, existed := bw.Versions[id]
		if existed {
			if sameVersion(old, r) {
				continue
			}
			return errors.New("worker version descriptors are immutable")
		}

		if r.Configuration != "" {
			tag, err := tx.Exec(ctx, `UPDATE krun_worker_configurations SET name=name WHERE configuration_id=$1 AND org_id=$2 AND worker_id=$3 AND env_name=$4`, r.Configuration, r.Organization, r.Worker, r.Environment)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return domain.Conflict("WORKER_CONFIGURATION_UNAVAILABLE", "Saved configuration is no longer available")
			}
		}
		variables, err := json.Marshal(r.Variables)
		if err != nil {
			return err
		}
		flags, err := json.Marshal(r.CompatibilityFlags)
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `INSERT INTO krun_worker_versions (version_id, org_id, worker_id, env_name, artifact_id, profile, archive_digest, module_digest, compatibility_date, compatibility_flags, config_revision, variables, secret_digest, created_at, version_metadata, assets_binding, tag, message, created_by, created_by_name, configuration_id, hyperdrive_bindings, service_bindings)
				SELECT $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,COALESCE($22::jsonb,'{}'::jsonb),COALESCE(NULLIF($23::jsonb,'null'::jsonb),'{}'::jsonb)
				WHERE EXISTS (SELECT 1 FROM krun_worker_artifacts
					WHERE artifact_id=$5 AND state='finalized' AND org_id=$2
					  AND profile=$6 AND archive_digest=$7 AND module_digest=$8
					  AND compatibility_date=$9 AND compatibility_flags=$10)
				ON CONFLICT (version_id) DO UPDATE SET version_id=excluded.version_id`, id, r.Organization, r.Worker, r.Environment, r.Artifact, r.Profile, r.ArchiveDigest, r.ModuleDigest, r.CompatibilityDate, flags, r.ConfigRevision, variables, r.SecretDigest, r.Created, r.VersionMetadata, r.AssetsBinding, r.Tag, r.Message, r.CreatedBy, r.CreatedByName, r.Configuration, r.HyperdriveBindings, r.ServiceBindings)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return domain.Conflict("ARTIFACT_UNAVAILABLE", "Artifact is no longer available")
		}
		if err := replaceWorkerVersionSecrets(ctx, tx, id, r.Worker, r.Environment, r.SecretVersions); err != nil {
			return err
		}
	}
	for id := range bw.Versions {
		if _, ok := aw.Versions[id]; !ok {
			if _, err := tx.Exec(ctx, `DELETE FROM krun_worker_version_secrets WHERE version_id=$1`, id); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `DELETE FROM krun_worker_versions WHERE version_id=$1`, id); err != nil {
				return err
			}
		}
	}
	return nil
}

func sameOperation(a, b domain.WorkerOperation) bool {
	if (a.Finished == nil) != (b.Finished == nil) {
		return false
	}
	if a.Finished != nil && !a.Finished.Equal(*b.Finished) {
		return false
	}
	return a.ID == b.ID && a.Kind == b.Kind && a.Worker == b.Worker && a.Organization == b.Organization &&
		a.Environment == b.Environment && a.State == b.State && a.Build == b.Build &&
		a.Version == b.Version && a.ExpectedRevision == b.ExpectedRevision &&
		a.ConfigRevision == b.ConfigRevision && a.EdgeRevision == b.EdgeRevision &&
		a.DeploymentRevision == b.DeploymentRevision && a.Error == b.Error &&
		reflect.DeepEqual(a.Steps, b.Steps) && a.Created.Equal(b.Created)
}

func writeOperations(ctx context.Context, tx pgx.Tx, bw, aw *domain.WorkerState) error {
	for id, op := range aw.Operations {
		old, existed := bw.Operations[id]
		if existed && sameOperation(old, op) {
			continue
		}
		steps, err := json.Marshal(op.Steps)
		if err != nil {
			return err
		}
		actor := aw.OperationPrivate[id].Actor
		if !existed {
			if _, err := tx.Exec(ctx, `INSERT INTO krun_worker_operations (operation_id, kind, org_id, worker_id, env_name, state, build_id, version_id, expected_revision, config_revision, edge_revision, deployment_revision, error, steps, actor_user_id, actor_key_id, actor_auth_version, created_at, finished_at, candidate_version_id, candidate_weight) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21)`, id, op.Kind, op.Organization, op.Worker, op.Environment, op.State, op.Build, op.Version, op.ExpectedRevision, op.ConfigRevision, op.EdgeRevision, op.DeploymentRevision, op.Error, steps, actor.User, actor.Key, actor.AuthVersion, op.Created, op.Finished, op.Candidate, op.CandidateWeight); err != nil {
				return err
			}
			continue
		}
		// A state transition is a compare-and-set: if another replica already
		// moved this row, this writer drops its change instead of fighting.
		if old.State != op.State {
			if _, err := tx.Exec(ctx, `UPDATE krun_worker_operations SET state=$3, build_id=$4, version_id=$5, expected_revision=$6, config_revision=$7, edge_revision=$8, deployment_revision=$9, error=$10, steps=$11, finished_at=$12 WHERE operation_id=$1 AND state=$2`, id, old.State, op.State, op.Build, op.Version, op.ExpectedRevision, op.ConfigRevision, op.EdgeRevision, op.DeploymentRevision, op.Error, steps, op.Finished); err != nil {
				return err
			}
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE krun_worker_operations SET build_id=$2, version_id=$3, expected_revision=$4, config_revision=$5, edge_revision=$6, deployment_revision=$7, error=$8, steps=$9, finished_at=$10 WHERE operation_id=$1`, id, op.Build, op.Version, op.ExpectedRevision, op.ConfigRevision, op.EdgeRevision, op.DeploymentRevision, op.Error, steps, op.Finished); err != nil {
			return err
		}
	}
	for id := range bw.Operations {
		if _, ok := aw.Operations[id]; !ok {
			if _, err := tx.Exec(ctx, `DELETE FROM krun_worker_operations WHERE operation_id=$1`, id); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeHistories(ctx context.Context, tx pgx.Tx, bw, aw *domain.WorkerState) error {
	for key, entries := range aw.Histories {
		worker, env := splitHistoryKey(key)
		for _, h := range entries {
			if _, err := tx.Exec(ctx, `INSERT INTO krun_worker_history (worker_id, env_name, revision, version_id, operation_id, kind, created_at) VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT (worker_id, env_name, revision) DO NOTHING`, worker, env, h.Revision, h.Version, h.Operation, h.Kind, h.Time); err != nil {
				return err
			}
		}
	}
	return nil
}

// splitHistoryKey splits "worker/env", the key the in-memory map uses.
func splitHistoryKey(key string) (string, string) {
	for i := len(key) - 1; i >= 0; i-- {
		if key[i] == '/' {
			return key[:i], key[i+1:]
		}
	}
	return key, ""
}

func writeIdempotency(ctx context.Context, tx pgx.Tx, bw, aw *domain.WorkerState) error {
	for key, record := range aw.Idempotency {
		if old, ok := bw.Idempotency[key]; ok {
			if old != record {
				return domain.Conflict("IDEMPOTENCY_CONFLICT", "Idempotency key was already used for another request")
			}
			continue
		}
		tag, err := tx.Exec(ctx, `INSERT INTO krun_worker_idempotency (idem_key, digest, target_id)
			VALUES ($1,$2,$3) ON CONFLICT (idem_key) DO NOTHING`, key, record.Digest, record.ID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return domain.Conflict("IDEMPOTENCY_CONFLICT", "Idempotency key is being handled by another request; retry")
		}
	}
	for key := range bw.Idempotency {
		if _, ok := aw.Idempotency[key]; !ok {
			if _, err := tx.Exec(ctx, `DELETE FROM krun_worker_idempotency WHERE idem_key=$1`, key); err != nil {
				return err
			}
		}
	}
	return nil
}

func sameServiceBindings(a, b map[string]domain.WorkerServiceBinding) bool {
	return maps.EqualFunc(a, b, func(left, right domain.WorkerServiceBinding) bool {
		return reflect.DeepEqual(left, right)
	})
}
