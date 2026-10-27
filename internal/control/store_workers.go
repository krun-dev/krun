package control

import (
	"context"
	"encoding/json"
	"reflect"

	"github.com/jackc/pgx/v5"
)

// persistWorkers writes only changed Workers rows. Operation and build state
// transitions use conditional updates so a concurrent claimer wins cleanly and
// this writer simply skips; that is the extent of the concurrency guarantee
// (AGENTS.md section 9: builds and releases converge eventually).
func (s *Store) persistWorkers(ctx context.Context, before, after *State) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = writeWorkersDiff(ctx, tx, before, after); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func writeWorkersDiff(ctx context.Context, tx pgx.Tx, before, after *State) error {
	bw, aw := before.Workers, after.Workers
	if bw == nil {
		bw = newWorkerState()
	}
	if aw == nil {
		aw = newWorkerState()
	}
	if err := writeServices(ctx, tx, bw, aw); err != nil {
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
	if err := writeIdempotency(ctx, tx, bw, aw); err != nil {
		return err
	}
	return writeRepositoryConnections(ctx, tx, before.RepositoryConnections, after.RepositoryConnections)
}

func writeServices(ctx context.Context, tx pgx.Tx, bw, aw *WorkerState) error {
	for id, svc := range aw.Services {
		old, existed := bw.Services[id]
		repositoryChanged := !existed || old.RepositoryRevision != svc.RepositoryRevision || !reflect.DeepEqual(old.Repository, svc.Repository)
		if repositoryChanged {
			var repository []byte
			if svc.Repository != nil {
				raw, err := json.Marshal(svc.Repository)
				if err != nil {
					return err
				}
				repository = raw
			}
			if _, err := tx.Exec(ctx, `INSERT INTO krun_worker_services (service_id, org_id, repository, repository_revision) VALUES ($1,$2,$3,$4) ON CONFLICT (service_id) DO UPDATE SET org_id=excluded.org_id, repository=excluded.repository, repository_revision=excluded.repository_revision, updated_at=now()`, id, svc.Organization, repository, svc.RepositoryRevision); err != nil {
				return err
			}
		}
		if !existed || !sameStrings(old.Domains, svc.Domains) {
			if err := replaceServiceDomains(ctx, tx, id, svc.Domains); err != nil {
				return err
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
			if _, err := tx.Exec(ctx, `INSERT INTO krun_worker_environments (service_id, env_name, enabled, config_revision, variables, release_revision, release_id, confirmed_release_id) VALUES ($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT (service_id, env_name) DO UPDATE SET enabled=excluded.enabled, config_revision=excluded.config_revision, variables=excluded.variables, release_revision=excluded.release_revision, release_id=excluded.release_id, confirmed_release_id=excluded.confirmed_release_id, updated_at=now()`, id, name, env.Enabled, env.ConfigRevision, variables, env.Revision, env.Release, env.Confirmed); err != nil {
				return err
			}
		}
		for name := range old.Environments {
			if _, ok := svc.Environments[name]; !ok {
				if _, err := tx.Exec(ctx, `DELETE FROM krun_worker_environments WHERE service_id=$1 AND env_name=$2`, id, name); err != nil {
					return err
				}
			}
		}
	}
	for id := range bw.Services {
		if _, ok := aw.Services[id]; ok {
			continue
		}
		for _, statement := range []string{
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
		reflect.DeepEqual(a.Variables, b.Variables)
}

func writeBuilds(ctx context.Context, tx pgx.Tx, bw, aw *WorkerState) error {
	for id, b := range aw.Builds {
		old, existed := bw.Builds[id]
		if existed && sameBuild(old, b) {
			continue
		}
		var repository, assets []byte
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
			if _, err := tx.Exec(ctx, `INSERT INTO krun_worker_builds (build_id, org_id, service_id, env_name, state, entrypoint, source_digest, profile, job, artifact_id, error, repository, assets, created_at, finished_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`, id, b.Organization, b.Service, b.Environment, b.State, b.Entrypoint, b.SourceDigest, b.Profile, b.Job, b.Artifact, b.Error, repository, assets, b.Created, b.Finished); err != nil {
				return err
			}
		} else if _, err := tx.Exec(ctx, `UPDATE krun_worker_builds SET org_id=$2, service_id=$3, env_name=$4, state=$5, entrypoint=$6, source_digest=$7, profile=$8, job=$9, artifact_id=$10, error=$11, repository=$12, assets=$13, created_at=$14, finished_at=$15 WHERE build_id=$1`, id, b.Organization, b.Service, b.Environment, b.State, b.Entrypoint, b.SourceDigest, b.Profile, b.Job, b.Artifact, b.Error, repository, assets, b.Created, b.Finished); err != nil {
			return err
		}
	}
	for id := range bw.Builds {
		if _, ok := aw.Builds[id]; ok {
			continue
		}
		if _, err := tx.Exec(ctx, `DELETE FROM krun_worker_builds WHERE build_id=$1`, id); err != nil {
			return err
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
		a.SourceDigest == b.SourceDigest && a.Profile == b.Profile && a.Job == b.Job &&
		a.Artifact == b.Artifact && a.Error == b.Error && a.Created.Equal(b.Created)
}

func sameBuildPrivate(a, b WorkerBuildPrivate) bool {
	return a.Actor == b.Actor && a.SourceKey == b.SourceKey && reflect.DeepEqual(a.Files, b.Files) && a.Log == b.Log
}

func writeArtifacts(ctx context.Context, tx pgx.Tx, bw, aw *WorkerState) error {
	for id, a := range aw.Artifacts {
		if old, ok := bw.Artifacts[id]; ok && old == a {
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO krun_worker_artifacts (artifact_id, org_id, state, archive_digest, manifest_digest, module_digest, size_bytes, object_key, storage_location, bucket, created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT (artifact_id) DO UPDATE SET state=excluded.state, archive_digest=excluded.archive_digest, manifest_digest=excluded.manifest_digest, module_digest=excluded.module_digest, size_bytes=excluded.size_bytes, object_key=excluded.object_key, storage_location=excluded.storage_location, bucket=excluded.bucket`, id, a.Organization, a.State, a.ArchiveDigest, a.ManifestDigest, a.ModuleDigest, a.Size, a.ObjectKey, a.StorageLocation, a.Bucket, a.Created); err != nil {
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

func sameRelease(a, b WorkerRelease) bool {
	return a.ID == b.ID && a.Service == b.Service && a.Organization == b.Organization &&
		a.Environment == b.Environment && a.Artifact == b.Artifact && a.ArchiveDigest == b.ArchiveDigest &&
		a.ModuleDigest == b.ModuleDigest && a.ConfigRevision == b.ConfigRevision &&
		reflect.DeepEqual(a.Variables, b.Variables) && a.Pool == b.Pool && a.Created.Equal(b.Created)
}

func writeReleases(ctx context.Context, tx pgx.Tx, bw, aw *WorkerState) error {
	for id, r := range aw.Releases {
		if old, ok := bw.Releases[id]; ok && sameRelease(old, r) {
			continue
		}
		variables, err := json.Marshal(r.Variables)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO krun_worker_releases (release_id, org_id, service_id, env_name, artifact_id, archive_digest, module_digest, config_revision, variables, pool, created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT (release_id) DO UPDATE SET archive_digest=excluded.archive_digest, module_digest=excluded.module_digest, config_revision=excluded.config_revision, variables=excluded.variables, pool=excluded.pool`, id, r.Organization, r.Service, r.Environment, r.Artifact, r.ArchiveDigest, r.ModuleDigest, r.ConfigRevision, variables, r.Pool, r.Created); err != nil {
			return err
		}
	}
	for id := range bw.Releases {
		if _, ok := aw.Releases[id]; !ok {
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
		if old, ok := bw.Idempotency[key]; ok && old == record {
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO krun_worker_idempotency (idem_key, digest, target_id) VALUES ($1,$2,$3) ON CONFLICT (idem_key) DO UPDATE SET digest=excluded.digest, target_id=excluded.target_id`, key, record.Digest, record.ID); err != nil {
			return err
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

func writeRepositoryConnections(ctx context.Context, tx pgx.Tx, before, after map[string]WorkerRepositoryConnection) error {
	for key, c := range after {
		if old, ok := before[key]; ok && old == c {
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO krun_repository_connections (user_id, provider_id, access_token, connected_at) VALUES ($1,$2,$3,$4) ON CONFLICT (user_id, provider_id) DO UPDATE SET access_token=excluded.access_token, connected_at=excluded.connected_at`, c.User, c.Provider, c.AccessToken, c.ConnectedAt); err != nil {
			return err
		}
	}
	for key, c := range before {
		if _, ok := after[key]; !ok {
			if _, err := tx.Exec(ctx, `DELETE FROM krun_repository_connections WHERE user_id=$1 AND provider_id=$2`, c.User, c.Provider); err != nil {
				return err
			}
		}
	}
	return nil
}
