package control

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
)

// Storage is one row per entity. State is no longer the persisted document: it
// is a working copy loaded from the rows a request may touch and written back
// as a diff, so unchanged rows are never rewritten.
//
// Two independent partitions exist because IAM and Workers mutations must not
// share a transaction (AGENTS.md section 9):
//
//	iam     — identity, organizations, permissions, resources, keys
//	workers — services, builds, releases, operations, repository links
//
// Sessions, browsers and login transactions belong to neither: they use
// single-row statements in store_auth.go, so the authenticated read path
// performs no snapshot write at all.

// loadIAM fills the identity and authorization half of st in one round trip.
// Authorization reads most of these tables on every request, so this is a batch
// rather than a sequence of queries.
func (s *Store) loadIAM(ctx context.Context, st *State) error {
	batch := &pgx.Batch{}
	batch.Queue(`SELECT user_id, source, subject, name, email, status, auth_version FROM krun_users`)
	batch.Queue(`SELECT org_id, name, revision FROM krun_organizations`)
	batch.Queue(`SELECT org_id, source FROM krun_org_sources ORDER BY source`)
	batch.Queue(`SELECT org_id, user_id FROM krun_members`)
	batch.Queue(`SELECT group_id, scope_kind, scope_id, name, revision FROM krun_groups`)
	batch.Queue(`SELECT group_id, user_id FROM krun_group_members`)
	batch.Queue(`SELECT assignment_id, subject_kind, subject_id, role, scope_kind, scope_id, conditions, revision FROM krun_assignments`)
	batch.Queue(`SELECT resource_id, kind, org_id, name, revision FROM krun_resources`)
	batch.Queue(`SELECT key_id, owner_user_id, org_id, scope_kind, name, prefix, policy, expires_at, revoked, revision FROM krun_api_keys`)
	batch.Queue(`SELECT source, subject, expires_at FROM krun_invitations`)
	batch.Queue(`SELECT source, fingerprint FROM krun_identity_sources`)
	batch.Queue(`SELECT provider_id, source, namespace FROM krun_identity_providers`)
	batch.Queue(`SELECT key, value FROM krun_meta`)

	results := s.pool.SendBatch(ctx, batch)
	defer results.Close()

	if err := readUsers(results, st); err != nil {
		return err
	}
	if err := readOrganizations(results, st); err != nil {
		return err
	}
	if err := readOrgSources(results, st); err != nil {
		return err
	}
	if err := readMembers(results, st); err != nil {
		return err
	}
	if err := readGroups(results, st); err != nil {
		return err
	}
	if err := readGroupMembers(results, st); err != nil {
		return err
	}
	if err := readAssignments(results, st); err != nil {
		return err
	}
	if err := readResources(results, st); err != nil {
		return err
	}
	if err := readAPIKeys(results, st); err != nil {
		return err
	}
	if err := readInvitations(results, st); err != nil {
		return err
	}
	if err := readIdentitySources(results, st); err != nil {
		return err
	}
	if err := readIdentityProviders(results, st); err != nil {
		return err
	}
	return readMeta(results, st)
}

func readUsers(results pgx.BatchResults, st *State) error {
	rows, err := results.Query()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var u User
		if err = rows.Scan(&u.ID, &u.Source, &u.Subject, &u.Name, &u.Email, &u.Status, &u.AuthVersion); err != nil {
			return err
		}
		st.Users[u.ID] = u
	}
	return rows.Err()
}

func readOrganizations(results pgx.BatchResults, st *State) error {
	rows, err := results.Query()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var o Organization
		if err = rows.Scan(&o.ID, &o.Name, &o.Revision); err != nil {
			return err
		}
		o.Sources = []string{}
		st.Organizations[o.ID] = o
	}
	return rows.Err()
}

func readOrgSources(results pgx.BatchResults, st *State) error {
	rows, err := results.Query()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var org, source string
		if err = rows.Scan(&org, &source); err != nil {
			return err
		}
		o := st.Organizations[org]
		o.Sources = append(o.Sources, source)
		st.Organizations[org] = o
	}
	return rows.Err()
}

func readMembers(results pgx.BatchResults, st *State) error {
	rows, err := results.Query()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var org, user string
		if err = rows.Scan(&org, &user); err != nil {
			return err
		}
		st.Members[memberKey(org, user)] = Member{Organization: org, User: user}
	}
	return rows.Err()
}

func readGroups(results pgx.BatchResults, st *State) error {
	rows, err := results.Query()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var g Group
		if err = rows.Scan(&g.ID, &g.ScopeKind, &g.ScopeID, &g.Name, &g.Revision); err != nil {
			return err
		}
		g.Members = []string{}
		st.Groups[g.ID] = g
	}
	return rows.Err()
}

func readGroupMembers(results pgx.BatchResults, st *State) error {
	rows, err := results.Query()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var gid, uid string
		if err = rows.Scan(&gid, &uid); err != nil {
			return err
		}
		g := st.Groups[gid]
		g.Members = append(g.Members, uid)
		st.Groups[gid] = g
	}
	return rows.Err()
}

func readAssignments(results pgx.BatchResults, st *State) error {
	rows, err := results.Query()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var a Assignment
		var conditions []byte
		if err = rows.Scan(&a.ID, &a.SubjectKind, &a.SubjectID, &a.Role, &a.ScopeKind, &a.ScopeID, &conditions, &a.Revision); err != nil {
			return err
		}
		if len(conditions) > 0 {
			if err = json.Unmarshal(conditions, &a.Conditions); err != nil {
				return err
			}
		}
		st.Assignments[a.ID] = a
	}
	return rows.Err()
}

func readResources(results pgx.BatchResults, st *State) error {
	rows, err := results.Query()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var r Resource
		if err = rows.Scan(&r.ID, &r.Kind, &r.Organization, &r.Name, &r.Revision); err != nil {
			return err
		}
		st.Resources[r.ID] = r
	}
	return rows.Err()
}

func readAPIKeys(results pgx.BatchResults, st *State) error {
	rows, err := results.Query()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var k APIKey
		var policy []byte
		if err = rows.Scan(&k.ID, &k.Owner, &k.Organization, &k.ScopeKind, &k.Name, &k.Prefix, &policy, &k.Expires, &k.Revoked, &k.Revision); err != nil {
			return err
		}
		if len(policy) > 0 {
			if err = json.Unmarshal(policy, &k.Policy); err != nil {
				return err
			}
		}
		st.Keys[k.ID] = k
	}
	return rows.Err()
}

func readInvitations(results pgx.BatchResults, st *State) error {
	rows, err := results.Query()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var source, subject string
		var expires = now()
		if err = rows.Scan(&source, &subject, &expires); err != nil {
			return err
		}
		st.Invitations[source+"\x00"+subject] = expires
	}
	return rows.Err()
}

func readIdentitySources(results pgx.BatchResults, st *State) error {
	rows, err := results.Query()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var source, fingerprint string
		if err = rows.Scan(&source, &fingerprint); err != nil {
			return err
		}
		st.Sources[source] = fingerprint
	}
	return rows.Err()
}

func readIdentityProviders(results pgx.BatchResults, st *State) error {
	rows, err := results.Query()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var providerID, source, namespace string
		if err = rows.Scan(&providerID, &source, &namespace); err != nil {
			return err
		}
		st.Sources["provider:"+providerID] = source
		st.Sources["namespace:"+namespace] = source
	}
	return rows.Err()
}

func readMeta(results pgx.BatchResults, st *State) error {
	rows, err := results.Query()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var key, value string
		if err = rows.Scan(&key, &value); err != nil {
			return err
		}
		switch key {
		case "revision":
			st.Revision = parseIntDefault(value)
		case "bootstrapped":
			st.Bootstrapped = value == "1"
		}
	}
	return rows.Err()
}

func parseIntDefault(raw string) int {
	n := 0
	for _, r := range raw {
		if r < '0' || r > '9' {
			return n
		}
		n = n*10 + int(r-'0')
	}
	return n
}

// loadWorkers fills the public Workers metadata in one round trip. Build
// source/log blobs and repository access tokens are intentionally fetched by
// key only at the endpoints that need them.
func (s *Store) loadWorkers(ctx context.Context, st *State) error {
	if st.Workers == nil {
		st.Workers = newWorkerState()
	}
	batch := &pgx.Batch{}
	batch.Queue(`SELECT service_id, org_id, network_profile, repository, repository_revision, domains_revision, preview_revision FROM krun_worker_services`)
	batch.Queue(`SELECT service_id, pool_id, org_id, network_profile, role, state, revision, created_at, updated_at FROM krun_worker_service_placements`)
	batch.Queue(`SELECT service_id, domain_id FROM krun_worker_service_domains ORDER BY service_id, ordinal`)
	batch.Queue(`SELECT service_id, env_name, enabled, config_revision, variables, release_revision, release_id, confirmed_release_id FROM krun_worker_environments`)
	batch.Queue(`SELECT secret_version_id, org_id, service_id, env_name, name, value_digest, key_version, created_by, created_at FROM krun_worker_secret_versions`)
	batch.Queue(`SELECT service_id, env_name, name, secret_version_id FROM krun_worker_environment_secrets`)
	batch.Queue(`SELECT build_id, org_id, service_id, env_name, state, entrypoint, source_digest, profile, job, artifact_id, error, repository, assets, created_at, finished_at, claimed_by, claimed_at FROM krun_worker_builds`)
	batch.Queue(`SELECT artifact_id, org_id, state, archive_digest, manifest_digest, module_digest, size_bytes, object_key, storage_location, bucket, created_at FROM krun_worker_artifacts`)
	batch.Queue(`SELECT release_id, org_id, service_id, env_name, artifact_id, archive_digest, module_digest, config_revision, variables, secret_digest, created_at FROM krun_worker_releases`)
	batch.Queue(`SELECT release_id, name, secret_version_id FROM krun_worker_release_secrets`)
	batch.Queue(`SELECT operation_id, kind, org_id, service_id, env_name, state, build_id, release_id, expected_revision, config_revision, edge_revision, release_revision, error, steps, actor_user_id, actor_key_id, actor_auth_version, created_at, finished_at FROM krun_worker_operations`)
	batch.Queue(`SELECT service_id, env_name, revision, release_id, operation_id, kind, created_at FROM krun_worker_history ORDER BY created_at DESC`)
	batch.Queue(`SELECT idem_key, digest, target_id FROM krun_worker_idempotency`)

	results := s.pool.SendBatch(ctx, batch)
	defer results.Close()

	for _, read := range []func(pgx.BatchResults, *State) error{
		readWorkerServices, readWorkerPlacements, readWorkerDomains, readWorkerEnvironments,
		readWorkerSecretVersions, readWorkerEnvironmentSecrets,
		readWorkerBuilds, readWorkerArtifacts,
		readWorkerReleases, readWorkerReleaseSecrets, readWorkerOperations, readWorkerHistory,
		readWorkerIdempotency,
	} {
		if err := read(results, st); err != nil {
			return err
		}
	}
	return nil
}

func readWorkerServices(results pgx.BatchResults, st *State) error {
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
		svc := WorkerService{
			Service: id, Organization: org, NetworkProfile: profile, RepositoryRevision: repositoryRevision,
			DomainsRevision: domainsRevision, PreviewRevision: previewRevision,
			Domains: []string{}, Environments: map[string]WorkerEnvironment{},
		}
		if len(repository) > 0 {
			var src WorkerRepositorySource
			if err = json.Unmarshal(repository, &src); err != nil {
				return err
			}
			svc.Repository = &src
		}
		ws.Services[id] = svc
	}
	return rows.Err()
}

func readWorkerPlacements(results pgx.BatchResults, st *State) error {
	rows, err := results.Query()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var placement WorkerPlacement
		if err = rows.Scan(&placement.Service, &placement.Pool, &placement.Organization, &placement.NetworkProfile,
			&placement.Role, &placement.State, &placement.Revision, &placement.Created, &placement.Updated); err != nil {
			return err
		}
		st.Workers.Placements[workerPlacementKey(placement.Service, placement.Pool)] = placement
	}
	return rows.Err()
}

func readWorkerDomains(results pgx.BatchResults, st *State) error {
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

func readWorkerEnvironments(results pgx.BatchResults, st *State) error {
	ws := st.Workers
	rows, err := results.Query()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var service, name string
		var env WorkerEnvironment
		var variables []byte
		if err = rows.Scan(&service, &name, &env.Enabled, &env.ConfigRevision, &variables, &env.Revision, &env.Release, &env.Confirmed); err != nil {
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
			svc.Environments = map[string]WorkerEnvironment{}
		}
		svc.Environments[name] = env
		ws.Services[service] = svc
	}
	return rows.Err()
}

func readWorkerSecretVersions(results pgx.BatchResults, st *State) error {
	rows, err := results.Query()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var version WorkerSecretVersion
		if err = rows.Scan(&version.ID, &version.Organization, &version.Service, &version.Environment,
			&version.Name, &version.ValueDigest, &version.KeyVersion, &version.CreatedBy, &version.Created); err != nil {
			return err
		}
		st.Workers.SecretVersions[version.ID] = version
	}
	return rows.Err()
}

func readWorkerEnvironmentSecrets(results pgx.BatchResults, st *State) error {
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
		if !ok {
			continue
		}
		if env.SecretVersions == nil {
			env.SecretVersions = map[string]string{}
		}
		env.SecretVersions[name] = version
		env.SecretNames = workerSecretNames(env.SecretVersions)
		item.Environments[environment] = env
		st.Workers.Services[service] = item
	}
	return rows.Err()
}

func readWorkerBuilds(results pgx.BatchResults, st *State) error {
	ws := st.Workers
	rows, err := results.Query()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var b WorkerBuild
		var repository, assets []byte
		if err = rows.Scan(&b.ID, &b.Organization, &b.Service, &b.Environment, &b.State, &b.Entrypoint, &b.SourceDigest, &b.Profile, &b.Job, &b.Artifact, &b.Error, &repository, &assets, &b.Created, &b.Finished, &b.ClaimedBy, &b.ClaimedAt); err != nil {
			return err
		}
		if len(repository) > 0 {
			var src WorkerBuildRepository
			if err = json.Unmarshal(repository, &src); err != nil {
				return err
			}
			b.Repository = &src
		}
		if len(assets) > 0 {
			var a WorkerAssetsConfig
			if err = json.Unmarshal(assets, &a); err != nil {
				return err
			}
			b.Assets = &a
		}
		ws.Builds[b.ID] = b
	}
	return rows.Err()
}

func readWorkerArtifacts(results pgx.BatchResults, st *State) error {
	ws := st.Workers
	rows, err := results.Query()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var a WorkerArtifact
		if err = rows.Scan(&a.ID, &a.Organization, &a.State, &a.ArchiveDigest, &a.ManifestDigest, &a.ModuleDigest, &a.Size, &a.ObjectKey, &a.StorageLocation, &a.Bucket, &a.Created); err != nil {
			return err
		}
		ws.Artifacts[a.ID] = a
	}
	return rows.Err()
}

func readWorkerReleases(results pgx.BatchResults, st *State) error {
	ws := st.Workers
	rows, err := results.Query()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var r WorkerRelease
		var variables []byte
		if err = rows.Scan(&r.ID, &r.Organization, &r.Service, &r.Environment, &r.Artifact, &r.ArchiveDigest, &r.ModuleDigest, &r.ConfigRevision, &variables, &r.SecretDigest, &r.Created); err != nil {
			return err
		}
		r.Variables = map[string]string{}
		r.SecretVersions = map[string]string{}
		if len(variables) > 0 {
			if err = json.Unmarshal(variables, &r.Variables); err != nil {
				return err
			}
		}
		ws.Releases[r.ID] = r
	}
	return rows.Err()
}

func readWorkerReleaseSecrets(results pgx.BatchResults, st *State) error {
	rows, err := results.Query()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var release, name, version string
		if err = rows.Scan(&release, &name, &version); err != nil {
			return err
		}
		item, ok := st.Workers.Releases[release]
		if !ok {
			continue
		}
		if item.SecretVersions == nil {
			item.SecretVersions = map[string]string{}
		}
		item.SecretVersions[name] = version
		st.Workers.Releases[release] = item
	}
	return rows.Err()
}

func readWorkerOperations(results pgx.BatchResults, st *State) error {
	ws := st.Workers
	rows, err := results.Query()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var op WorkerOperation
		var steps []byte
		var actor WorkerActor
		if err = rows.Scan(&op.ID, &op.Kind, &op.Organization, &op.Service, &op.Environment, &op.State, &op.Build, &op.Release, &op.ExpectedRevision, &op.ConfigRevision, &op.EdgeRevision, &op.ReleaseRevision, &op.Error, &steps, &actor.User, &actor.Key, &actor.AuthVersion, &op.Created, &op.Finished); err != nil {
			return err
		}
		if len(steps) > 0 {
			if err = json.Unmarshal(steps, &op.Steps); err != nil {
				return err
			}
		}
		ws.Operations[op.ID] = op
		ws.OperationPrivate[op.ID] = WorkerOperationPrivate{Actor: actor}
	}
	return rows.Err()
}

func readWorkerHistory(results pgx.BatchResults, st *State) error {
	ws := st.Workers
	rows, err := results.Query()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var service, env string
		var h WorkerHistory
		if err = rows.Scan(&service, &env, &h.Revision, &h.Release, &h.Operation, &h.Kind, &h.Time); err != nil {
			return err
		}
		key := service + "/" + env
		ws.Histories[key] = append(ws.Histories[key], h)
	}
	return rows.Err()
}

func readWorkerIdempotency(results pgx.BatchResults, st *State) error {
	ws := st.Workers
	rows, err := results.Query()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var record WorkerIdempotency
		if err = rows.Scan(&key, &record.Digest, &record.ID); err != nil {
			return err
		}
		ws.Idempotency[key] = record
	}
	return rows.Err()
}

var errNoState = errors.New("state is not initialized")
