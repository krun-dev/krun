package control

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// persistWorkers writes only changed Workers rows. Operation and build state
// transitions use conditional updates so a concurrent claimer wins cleanly and
// this writer simply skips; that is the extent of the concurrency guarantee
// (AGENTS.md section 9: builds and releases converge eventually).
func (s *Store) persistWorkers(ctx context.Context, before, after *State, audits []Audit) error {
	topologyChanged := workerEdgeTopologyChanged(before, after)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = writeWorkersDiff(ctx, tx, before, after); err != nil {
		return err
	}
	if err = insertAudits(ctx, tx, audits); err != nil {
		return err
	}
	if topologyChanged {
		if err = bumpWorkerTopologyGeneration(ctx, tx); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func bumpWorkerTopologyGeneration(ctx context.Context, tx interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}) error {
	_, err := tx.Exec(ctx, `INSERT INTO krun_meta (key, value) VALUES ('worker_topology_generation','1')
		ON CONFLICT (key) DO UPDATE SET value=(krun_meta.value::bigint + 1)::text`)
	return err
}

// workerEdgeTopologyChanged mirrors workerConfigFromState's durable inputs.
// Build/operation/log mutations intentionally do not wake config publishers.
func workerEdgeTopologyChanged(before, after *State) bool {
	bw, aw := before.Workers, after.Workers
	if bw == nil {
		bw = newWorkerState()
	}
	if aw == nil {
		aw = newWorkerState()
	}
	if len(bw.Services) != len(aw.Services) {
		return true
	}
	referenced := map[string]bool{}
	for id, current := range aw.Services {
		previous, ok := bw.Services[id]
		if !ok || current.Service != previous.Service || current.Organization != previous.Organization || current.NetworkProfile != previous.NetworkProfile || !sameStrings(current.Domains, previous.Domains) || len(current.Environments) != len(previous.Environments) {
			return true
		}
		for name, env := range current.Environments {
			old, ok := previous.Environments[name]
			if !ok || env.Enabled != old.Enabled || env.Release != old.Release {
				return true
			}
			if env.Release != "" {
				referenced[env.Release] = true
			}
			if old.Release != "" {
				referenced[old.Release] = true
			}
		}
	}
	if !sameWorkerPlacements(bw.Placements, aw.Placements) {
		return true
	}
	for id := range referenced {
		old, oldOK := bw.Releases[id]
		current, currentOK := aw.Releases[id]
		if oldOK != currentOK || oldOK && !sameRelease(old, current) {
			return true
		}
	}
	return false
}

func writeWorkersDiff(ctx context.Context, tx pgx.Tx, before, after *State) error {
	bw, aw := before.Workers, after.Workers
	if bw == nil {
		bw = newWorkerState()
	}
	if aw == nil {
		aw = newWorkerState()
	}
	// Reserve idempotency keys before inserting their build/operation rows.
	// The unique key is the cross-replica arbiter; a losing request rolls this
	// transaction back before it can create a second target.
	if err := writeIdempotency(ctx, tx, bw, aw); err != nil {
		return err
	}
	if err := writeWorkerSecretVersions(ctx, tx, bw, aw, after.pendingWorkerSecrets); err != nil {
		return err
	}
	if err := writeServices(ctx, tx, bw, aw); err != nil {
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
	if err := writeReleases(ctx, tx, bw, aw); err != nil {
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
func writeServices(ctx context.Context, tx pgx.Tx, bw, aw *WorkerState) error {
	for id, svc := range aw.Services {
		old, existed := bw.Services[id]
		repositoryChanged := !existed || old.RepositoryRevision != svc.RepositoryRevision || !reflect.DeepEqual(old.Repository, svc.Repository)
		domainsChanged := existed && old.DomainsRevision != svc.DomainsRevision
		profileChanged := existed && old.NetworkProfile != svc.NetworkProfile
		previewSetChanged := !existed && previewCount(svc.Environments) > 0 || existed && !samePreviewSet(old.Environments, svc.Environments)
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
				tag, err := tx.Exec(ctx, `INSERT INTO krun_worker_services
					(service_id, org_id, network_profile, repository, repository_revision, domains_revision, preview_revision)
					VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT (service_id) DO NOTHING`, id, svc.Organization,
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
					return problem(409, "REVISION_CONFLICT", "Repository configuration changed")
				}
			} else {
				if svc.RepositoryRevision != old.RepositoryRevision+1 {
					return problem(409, "REVISION_CONFLICT", "Repository configuration changed")
				}
				tag, err := tx.Exec(ctx, `UPDATE krun_worker_services
					SET repository=$2, repository_revision=$3, updated_at=now()
					WHERE service_id=$1 AND repository_revision=$4`, id, repository, svc.RepositoryRevision, old.RepositoryRevision)
				if err != nil {
					return err
				}
				if tag.RowsAffected() != 1 {
					return problem(409, "REVISION_CONFLICT", "Repository configuration changed")
				}
			}
		}
		if existed && old.DomainsRevision != svc.DomainsRevision {
			if svc.DomainsRevision != old.DomainsRevision+1 {
				return problem(409, "REVISION_CONFLICT", "Domain configuration changed")
			}
			tag, err := tx.Exec(ctx, `UPDATE krun_worker_services
				SET domains_revision=$2, network_profile=$4, updated_at=now()
				WHERE service_id=$1 AND domains_revision=$3`, id, svc.DomainsRevision, old.DomainsRevision, svc.NetworkProfile)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return problem(409, "REVISION_CONFLICT", "Domain configuration changed")
			}
		}
		if profileChanged && !domainsChanged {
			tag, err := tx.Exec(ctx, `UPDATE krun_worker_services SET network_profile=$2, updated_at=now()
				WHERE service_id=$1 AND network_profile=$3`, id, svc.NetworkProfile, old.NetworkProfile)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return problem(409, "REVISION_CONFLICT", "Runtime placement changed")
			}
		}
		if domainsChanged {
			if err := replaceServiceDomains(ctx, tx, id, svc.Domains); err != nil {
				return err
			}
		}
		if existed && previewSetChanged {
			if svc.PreviewRevision != old.PreviewRevision+1 {
				return problem(409, "REVISION_CONFLICT", "Preview environments changed")
			}
			tag, err := tx.Exec(ctx, `UPDATE krun_worker_services
				SET preview_revision=$2, updated_at=now()
				WHERE service_id=$1 AND preview_revision=$3`, id, svc.PreviewRevision, old.PreviewRevision)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return problem(409, "REVISION_CONFLICT", "Preview environments changed")
			}
		}
		for name, env := range svc.Environments {
			oldEnv, ok := old.Environments[name]
			if ok && sameEnvironment(oldEnv, env) {
				continue
			}
			variables, err := json.Marshal(env.Variables)
			if err != nil {
				return err
			}
			if !ok {
				tag, err := tx.Exec(ctx, `INSERT INTO krun_worker_environments
					(service_id, env_name, enabled, config_revision, variables, release_revision, release_id, confirmed_release_id)
					VALUES ($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT (service_id, env_name) DO NOTHING`,
					id, name, env.Enabled, env.ConfigRevision, variables, env.Revision, env.Release, env.Confirmed)
				if err != nil {
					return err
				}
				if tag.RowsAffected() == 0 && !defaultWorkerBaseline(name, env) {
					return problem(409, "REVISION_CONFLICT", "Environment configuration changed")
				}
			} else {
				tag, err := tx.Exec(ctx, `UPDATE krun_worker_environments
					SET enabled=$3, config_revision=$4, variables=$5, release_revision=$6,
					    release_id=$7, confirmed_release_id=$8, updated_at=now()
					WHERE service_id=$1 AND env_name=$2 AND config_revision=$9
					  AND release_revision=$10 AND release_id=$11 AND confirmed_release_id=$12`,
					id, name, env.Enabled, env.ConfigRevision, variables, env.Revision, env.Release, env.Confirmed,
					oldEnv.ConfigRevision, oldEnv.Revision, oldEnv.Release, oldEnv.Confirmed)
				if err != nil {
					return err
				}
				if tag.RowsAffected() != 1 {
					return problem(409, "REVISION_CONFLICT", "Environment configuration changed")
				}
			}
			if err := replaceWorkerEnvironmentSecrets(ctx, tx, id, name, env.SecretVersions); err != nil {
				return err
			}
		}
		for name, oldEnv := range old.Environments {
			if _, ok := svc.Environments[name]; !ok {
				if _, err := tx.Exec(ctx, `DELETE FROM krun_worker_environment_secrets WHERE service_id=$1 AND env_name=$2`, id, name); err != nil {
					return err
				}
				tag, err := tx.Exec(ctx, `DELETE FROM krun_worker_environments
					WHERE service_id=$1 AND env_name=$2 AND config_revision=$3
					  AND release_revision=$4 AND release_id=$5 AND confirmed_release_id=$6`,
					id, name, oldEnv.ConfigRevision, oldEnv.Revision, oldEnv.Release, oldEnv.Confirmed)
				if err != nil {
					return err
				}
				if tag.RowsAffected() != 1 {
					return problem(409, "REVISION_CONFLICT", "Environment configuration changed")
				}
			}
		}
	}
	for id := range bw.Services {
		if _, ok := aw.Services[id]; ok {
			continue
		}
		for _, statement := range []string{
			`DELETE FROM krun_worker_environment_secrets WHERE service_id=$1`,
			`DELETE FROM krun_worker_service_domains WHERE service_id=$1`,
			`DELETE FROM krun_worker_environments WHERE service_id=$1`,
		} {
			if _, err := tx.Exec(ctx, statement, id); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `DELETE FROM krun_worker_services WHERE service_id=$1`, id); err != nil {
			return err
		}
	}
	return nil
}

func writePlacements(ctx context.Context, tx pgx.Tx, bw, aw *WorkerState) error {
	// Remove superseded primaries before inserting their replacements so the
	// partial unique index remains the arbiter for one live primary.
	for key, placement := range bw.Placements {
		if _, ok := aw.Placements[key]; ok {
			continue
		}
		tag, err := tx.Exec(ctx, `DELETE FROM krun_worker_service_placements
			WHERE service_id=$1 AND pool_id=$2 AND revision=$3`, placement.Service, placement.Pool, placement.Revision)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return problem(409, "REVISION_CONFLICT", "Runtime placement changed")
		}
	}
	for key, placement := range aw.Placements {
		old, existed := bw.Placements[key]
		if existed && sameWorkerPlacement(old, placement) {
			continue
		}
		if !existed {
			_, err := tx.Exec(ctx, `INSERT INTO krun_worker_service_placements
				(service_id, pool_id, org_id, network_profile, role, state, revision, created_at, updated_at)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, placement.Service, placement.Pool,
				placement.Organization, placement.NetworkProfile, placement.Role, placement.State,
				placement.Revision, placement.Created, placement.Updated)
			if err != nil {
				return err
			}
			continue
		}
		tag, err := tx.Exec(ctx, `UPDATE krun_worker_service_placements
			SET org_id=$3, network_profile=$4, role=$5, state=$6, revision=$7, updated_at=$8
			WHERE service_id=$1 AND pool_id=$2 AND revision=$9`, placement.Service, placement.Pool,
			placement.Organization, placement.NetworkProfile, placement.Role, placement.State,
			placement.Revision, placement.Updated, old.Revision)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return problem(409, "REVISION_CONFLICT", "Runtime placement changed")
		}
	}
	return nil
}

func sameWorkerPlacements(a, b map[string]WorkerPlacement) bool {
	if len(a) != len(b) {
		return false
	}
	for key, placement := range a {
		other, ok := b[key]
		if !ok || !sameWorkerPlacement(placement, other) {
			return false
		}
	}
	return true
}

func sameWorkerPlacement(a, b WorkerPlacement) bool {
	return a.Service == b.Service && a.Organization == b.Organization &&
		a.NetworkProfile == b.NetworkProfile && a.Pool == b.Pool && a.Role == b.Role &&
		a.State == b.State && a.Revision == b.Revision && a.Created.Equal(b.Created) && a.Updated.Equal(b.Updated)
}

// The baseline row is lazily materialised. Two independent first requests may
// race to insert this exact value; the loser can safely continue because it did
// not intend to change environment configuration.
func defaultWorkerBaseline(name string, env WorkerEnvironment) bool {
	return name == workerBaseline && env.Name == workerBaseline && env.Enabled &&
		env.ConfigRevision == 1 && env.Revision == 0 && env.Release == "" && env.Confirmed == "" &&
		len(env.Variables) == 0 && len(env.SecretVersions) == 0
}

func previewCount(environments map[string]WorkerEnvironment) int {
	count := 0
	for name := range environments {
		if name != workerBaseline {
			count++
		}
	}
	return count
}

func samePreviewSet(a, b map[string]WorkerEnvironment) bool {
	if previewCount(a) != previewCount(b) {
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

func replaceServiceDomains(ctx context.Context, tx pgx.Tx, service string, domains []string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM krun_worker_service_domains WHERE service_id=$1`, service); err != nil {
		return err
	}
	for i, domain := range domains {
		if _, err := tx.Exec(ctx, `INSERT INTO krun_worker_service_domains (service_id, domain_id, ordinal) VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, service, domain, i); err != nil {
			return err
		}
	}
	return nil
}

func sameEnvironment(a, b WorkerEnvironment) bool {
	return a.Name == b.Name && a.Enabled == b.Enabled && a.ConfigRevision == b.ConfigRevision &&
		a.Revision == b.Revision && a.Release == b.Release && a.Confirmed == b.Confirmed &&
		reflect.DeepEqual(a.Variables, b.Variables) && reflect.DeepEqual(a.SecretVersions, b.SecretVersions)
}

func writeBuilds(ctx context.Context, tx pgx.Tx, bw, aw *WorkerState) error {
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
				tag, err := tx.Exec(ctx, `UPDATE krun_worker_services
					SET active_build_count=active_build_count+1, updated_at=now()
					WHERE service_id=$1 AND active_build_count < $2`, b.Service, workerActiveBuildLimit)
				if err != nil {
					return err
				}
				if tag.RowsAffected() != 1 {
					return problem(409, "BUILD_LIMIT", "At most 100 builds may be active per service")
				}
			}
			if private, ok := aw.BuildPrivate[id]; ok && private.SourceKey != "" {
				if err := reserveWorkerSourceUpload(ctx, tx, private.SourceKey, b.Organization, id); err != nil {
					return err
				}
			}
			if _, err := tx.Exec(ctx, `INSERT INTO krun_worker_builds (build_id, org_id, service_id, env_name, state, entrypoint, source_digest, profile, compatibility_date, compatibility_flags, job, artifact_id, error, repository, assets, created_at, finished_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`, id, b.Organization, b.Service, b.Environment, b.State, b.Entrypoint, b.SourceDigest, b.Profile, b.CompatibilityDate, flags, b.Job, b.Artifact, b.Error, repository, assets, b.Created, b.Finished); err != nil {
				return err
			}
		} else {
			tag, err := tx.Exec(ctx, `UPDATE krun_worker_builds
				SET org_id=$2, service_id=$3, env_name=$4, state=$5, entrypoint=$6,
				    source_digest=$7, profile=$8, compatibility_date=$9, compatibility_flags=$10,
				    job=$11, artifact_id=$12, error=$13, repository=$14, assets=$15,
				    created_at=$16, finished_at=$17
				WHERE build_id=$1 AND state=$18 AND claimed_by=$19
				  AND claimed_at IS NOT DISTINCT FROM $20`, id, b.Organization, b.Service,
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
		var deletedState, deletedService string
		old := bw.Builds[id]
		err := tx.QueryRow(ctx, `DELETE FROM krun_worker_builds
			WHERE build_id=$1 AND state=$2 AND claimed_by=$3
			  AND claimed_at IS NOT DISTINCT FROM $4
			RETURNING state, service_id`, id, old.State, old.ClaimedBy, old.ClaimedAt).Scan(&deletedState, &deletedService)
		if errors.Is(err, pgx.ErrNoRows) {
			return errors.New("build changed while deleting snapshot")
		}
		if err != nil {
			return err
		}
		if !workerBuildTerminal(deletedState) {
			if _, err := tx.Exec(ctx, `UPDATE krun_worker_services
				SET active_build_count=GREATEST(active_build_count-1,0), updated_at=now()
				WHERE service_id=$1`, deletedService); err != nil {
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
		if _, err := tx.Exec(ctx, `INSERT INTO krun_worker_build_private (build_id, actor_user_id, actor_key_id, actor_auth_version, source_key, files, log) VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT (build_id) DO UPDATE SET actor_user_id=excluded.actor_user_id, actor_key_id=excluded.actor_key_id, actor_auth_version=excluded.actor_auth_version, source_key=excluded.source_key, files=excluded.files, log=excluded.log`, id, p.Actor.User, p.Actor.Key, p.Actor.AuthVersion, p.SourceKey, files, p.Log); err != nil {
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

const workerActiveBuildLimit = 100

func workerBuildTerminal(state string) bool {
	return state == "succeeded" || state == "failed"
}

func sameBuild(a, b WorkerBuild) bool {
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
	return a.ID == b.ID && a.Organization == b.Organization && a.Service == b.Service &&
		a.Environment == b.Environment && a.State == b.State && a.Entrypoint == b.Entrypoint &&
		a.SourceDigest == b.SourceDigest && a.Profile == b.Profile &&
		a.CompatibilityDate == b.CompatibilityDate && sameStrings(a.CompatibilityFlags, b.CompatibilityFlags) && a.Job == b.Job &&
		a.Artifact == b.Artifact && a.Error == b.Error && a.Created.Equal(b.Created)
}

func sameBuildPrivate(a, b WorkerBuildPrivate) bool {
	return a.Actor == b.Actor && a.SourceKey == b.SourceKey && reflect.DeepEqual(a.Files, b.Files) && a.Log == b.Log
}

// workerBuildPrivate fetches one potentially large source/log row. List and
// metadata endpoints deliberately never materialize this table.
func (s *Store) workerBuildPrivate(ctx context.Context, id string) (WorkerBuildPrivate, bool, error) {
	if s.pool == nil {
		private, ok := s.memState().Workers.BuildPrivate[id]
		return private, ok, nil
	}
	var private WorkerBuildPrivate
	var files []byte
	err := s.pool.QueryRow(ctx, `SELECT actor_user_id, actor_key_id, actor_auth_version, source_key, files, log
        FROM krun_worker_build_private WHERE build_id=$1`, id).
		Scan(&private.Actor.User, &private.Actor.Key, &private.Actor.AuthVersion, &private.SourceKey, &files, &private.Log)
	if errors.Is(err, pgx.ErrNoRows) {
		return WorkerBuildPrivate{}, false, nil
	}
	if err != nil {
		return WorkerBuildPrivate{}, false, err
	}
	if len(files) > 0 {
		if err = json.Unmarshal(files, &private.Files); err != nil {
			return WorkerBuildPrivate{}, false, err
		}
	}
	return private, true, nil
}

func writeArtifacts(ctx context.Context, tx pgx.Tx, bw, aw *WorkerState) error {
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

func sameArtifact(a, b WorkerArtifact) bool {
	return a.ID == b.ID && a.Organization == b.Organization && a.State == b.State &&
		a.Profile == b.Profile && a.ArchiveDigest == b.ArchiveDigest &&
		a.ManifestDigest == b.ManifestDigest && a.ModuleDigest == b.ModuleDigest &&
		a.CompatibilityDate == b.CompatibilityDate && sameStrings(a.CompatibilityFlags, b.CompatibilityFlags) &&
		a.Size == b.Size && a.ObjectKey == b.ObjectKey && a.StorageLocation == b.StorageLocation &&
		a.Bucket == b.Bucket && a.Created.Equal(b.Created) && a.UseRevision == b.UseRevision
}

func sameArtifactContent(a, b WorkerArtifact) bool {
	return a.ID == b.ID && a.Organization == b.Organization && a.Profile == b.Profile &&
		a.ArchiveDigest == b.ArchiveDigest && a.ManifestDigest == b.ManifestDigest &&
		a.ModuleDigest == b.ModuleDigest && a.CompatibilityDate == b.CompatibilityDate &&
		sameStrings(a.CompatibilityFlags, b.CompatibilityFlags) && a.Size == b.Size &&
		a.ObjectKey == b.ObjectKey && a.StorageLocation == b.StorageLocation && a.Bucket == b.Bucket
}

func sameRelease(a, b WorkerRelease) bool {
	return a.ID == b.ID && a.Service == b.Service && a.Organization == b.Organization &&
		a.Environment == b.Environment && a.Artifact == b.Artifact && a.Profile == b.Profile && a.ArchiveDigest == b.ArchiveDigest &&
		a.ModuleDigest == b.ModuleDigest && a.CompatibilityDate == b.CompatibilityDate &&
		sameStrings(a.CompatibilityFlags, b.CompatibilityFlags) && a.ConfigRevision == b.ConfigRevision &&
		reflect.DeepEqual(a.Variables, b.Variables) && a.SecretDigest == b.SecretDigest &&
		reflect.DeepEqual(a.SecretVersions, b.SecretVersions) && a.Created.Equal(b.Created)
}

func writeReleases(ctx context.Context, tx pgx.Tx, bw, aw *WorkerState) error {
	for id, r := range aw.Releases {
		old, existed := bw.Releases[id]
		if existed {
			if sameRelease(old, r) {
				continue
			}
			return errors.New("worker release descriptors are immutable")
		}
		variables, err := json.Marshal(r.Variables)
		if err != nil {
			return err
		}
		flags, err := json.Marshal(r.CompatibilityFlags)
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `INSERT INTO krun_worker_releases (release_id, org_id, service_id, env_name, artifact_id, profile, archive_digest, module_digest, compatibility_date, compatibility_flags, config_revision, variables, secret_digest, created_at)
				SELECT $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14
				WHERE EXISTS (SELECT 1 FROM krun_worker_artifacts
					WHERE artifact_id=$5 AND state='finalized' AND org_id=$2
					  AND profile=$6 AND archive_digest=$7 AND module_digest=$8
					  AND compatibility_date=$9 AND compatibility_flags=$10)
				ON CONFLICT (release_id) DO UPDATE SET release_id=excluded.release_id`, id, r.Organization, r.Service, r.Environment, r.Artifact, r.Profile, r.ArchiveDigest, r.ModuleDigest, r.CompatibilityDate, flags, r.ConfigRevision, variables, r.SecretDigest, r.Created)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return problem(409, "ARTIFACT_UNAVAILABLE", "Artifact is no longer available")
		}
		if err := replaceWorkerReleaseSecrets(ctx, tx, id, r.Service, r.Environment, r.SecretVersions); err != nil {
			return err
		}
	}
	for id := range bw.Releases {
		if _, ok := aw.Releases[id]; !ok {
			if _, err := tx.Exec(ctx, `DELETE FROM krun_worker_release_secrets WHERE release_id=$1`, id); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `DELETE FROM krun_worker_releases WHERE release_id=$1`, id); err != nil {
				return err
			}
		}
	}
	return nil
}

func sameOperation(a, b WorkerOperation) bool {
	if (a.Finished == nil) != (b.Finished == nil) {
		return false
	}
	if a.Finished != nil && !a.Finished.Equal(*b.Finished) {
		return false
	}
	return a.ID == b.ID && a.Kind == b.Kind && a.Service == b.Service && a.Organization == b.Organization &&
		a.Environment == b.Environment && a.State == b.State && a.Build == b.Build &&
		a.Release == b.Release && a.ExpectedRevision == b.ExpectedRevision &&
		a.ConfigRevision == b.ConfigRevision && a.EdgeRevision == b.EdgeRevision &&
		a.ReleaseRevision == b.ReleaseRevision && a.Error == b.Error &&
		reflect.DeepEqual(a.Steps, b.Steps) && a.Created.Equal(b.Created)
}

func writeOperations(ctx context.Context, tx pgx.Tx, bw, aw *WorkerState) error {
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
			if _, err := tx.Exec(ctx, `INSERT INTO krun_worker_operations (operation_id, kind, org_id, service_id, env_name, state, build_id, release_id, expected_revision, config_revision, edge_revision, release_revision, error, steps, actor_user_id, actor_key_id, actor_auth_version, created_at, finished_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)`, id, op.Kind, op.Organization, op.Service, op.Environment, op.State, op.Build, op.Release, op.ExpectedRevision, op.ConfigRevision, op.EdgeRevision, op.ReleaseRevision, op.Error, steps, actor.User, actor.Key, actor.AuthVersion, op.Created, op.Finished); err != nil {
				return err
			}
			continue
		}
		// A state transition is a compare-and-set: if another replica already
		// moved this row, this writer drops its change instead of fighting.
		if old.State != op.State {
			if _, err := tx.Exec(ctx, `UPDATE krun_worker_operations SET state=$3, build_id=$4, release_id=$5, expected_revision=$6, config_revision=$7, edge_revision=$8, release_revision=$9, error=$10, steps=$11, finished_at=$12 WHERE operation_id=$1 AND state=$2`, id, old.State, op.State, op.Build, op.Release, op.ExpectedRevision, op.ConfigRevision, op.EdgeRevision, op.ReleaseRevision, op.Error, steps, op.Finished); err != nil {
				return err
			}
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE krun_worker_operations SET build_id=$2, release_id=$3, expected_revision=$4, config_revision=$5, edge_revision=$6, release_revision=$7, error=$8, steps=$9, finished_at=$10 WHERE operation_id=$1`, id, op.Build, op.Release, op.ExpectedRevision, op.ConfigRevision, op.EdgeRevision, op.ReleaseRevision, op.Error, steps, op.Finished); err != nil {
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

func writeHistories(ctx context.Context, tx pgx.Tx, bw, aw *WorkerState) error {
	for key, entries := range aw.Histories {
		service, env := splitHistoryKey(key)
		for _, h := range entries {
			if _, err := tx.Exec(ctx, `INSERT INTO krun_worker_history (service_id, env_name, revision, release_id, operation_id, kind, created_at) VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT (service_id, env_name, revision) DO NOTHING`, service, env, h.Revision, h.Release, h.Operation, h.Kind, h.Time); err != nil {
				return err
			}
		}
	}
	return nil
}

// splitHistoryKey splits "service/env", the key the in-memory map uses.
func splitHistoryKey(key string) (string, string) {
	for i := len(key) - 1; i >= 0; i-- {
		if key[i] == '/' {
			return key[:i], key[i+1:]
		}
	}
	return key, ""
}

func writeIdempotency(ctx context.Context, tx pgx.Tx, bw, aw *WorkerState) error {
	for key, record := range aw.Idempotency {
		if old, ok := bw.Idempotency[key]; ok {
			if old != record {
				return problem(409, "IDEMPOTENCY_CONFLICT", "Idempotency key was already used for another request")
			}
			continue
		}
		tag, err := tx.Exec(ctx, `INSERT INTO krun_worker_idempotency (idem_key, digest, target_id)
			VALUES ($1,$2,$3) ON CONFLICT (idem_key) DO NOTHING`, key, record.Digest, record.ID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return problem(409, "IDEMPOTENCY_CONFLICT", "Idempotency key is being handled by another request; retry")
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
