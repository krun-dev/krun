package control

import (
	"bytes"
	"context"
	"testing"
)

func TestWorkerSecretsPersistEncryptedAndRelational(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	config := testWorkersConfig()
	enableWorkerSecrets(config)
	encrypted, err := encryptWorkerSecret(config, "org-secret", "svc-secret", "production", "API_TOKEN", "admin", "database-must-not-see-plaintext")
	if err != nil {
		t.Fatal(err)
	}
	refs := map[string]string{"API_TOKEN": encrypted.Version.ID}
	release := WorkerRelease{
		ID: "rel-secret-postgres", Organization: "org-secret", Service: "svc-secret", Environment: "production",
		Artifact: "art-secret-postgres", ArchiveDigest: "archive", ModuleDigest: "module",
		ConfigRevision: 2, Variables: map[string]string{}, SecretDigest: workerSecretsDigest(refs),
		SecretVersions: refs, Pool: "default", Created: now(),
	}
	if err = store.Update(ctx, PartitionWorkers, func(st *State) error {
		st.Workers.SecretVersions[encrypted.Version.ID] = encrypted.Version
		st.pendingWorkerSecrets = map[string]encryptedWorkerSecret{encrypted.Version.ID: encrypted}
		st.Workers.Services[release.Service] = WorkerService{
			Service: release.Service, Organization: release.Organization,
			Environments: map[string]WorkerEnvironment{
				"production": {Name: "production", Enabled: true, ConfigRevision: 2, Variables: map[string]string{}, SecretNames: []string{"API_TOKEN"}, SecretVersions: refs},
			},
		}
		st.Workers.Artifacts[release.Artifact] = WorkerArtifact{ID: release.Artifact, Organization: release.Organization, State: "finalized", Created: now()}
		st.Workers.Releases[release.ID] = release
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	var ciphertext []byte
	if err = store.pool.QueryRow(ctx, `SELECT ciphertext FROM krun_worker_secret_versions WHERE secret_version_id=$1`, encrypted.Version.ID).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ciphertext, []byte("database-must-not-see-plaintext")) {
		t.Fatal("plaintext persisted in PostgreSQL")
	}
	loaded, err := store.Open(ctx, PartitionWorkers)
	if err != nil {
		t.Fatal(err)
	}
	environment := loaded.Workers.Services[release.Service].Environments["production"]
	if environment.SecretVersions["API_TOKEN"] != encrypted.Version.ID || loaded.Workers.Releases[release.ID].SecretVersions["API_TOKEN"] != encrypted.Version.ID {
		t.Fatalf("relational bindings not reloaded: env=%v release=%v", environment.SecretVersions, loaded.Workers.Releases[release.ID].SecretVersions)
	}
	digest, values, err := store.encryptedReleaseSecrets(ctx, release.ID, "default")
	if err != nil || digest != release.SecretDigest || len(values) != 1 {
		t.Fatalf("runtime lookup: digest=%q values=%d err=%v", digest, len(values), err)
	}
	plain, err := decryptWorkerSecret(config, values[0])
	if err != nil || plain != "database-must-not-see-plaintext" {
		t.Fatalf("decrypt persisted secret: %q %v", plain, err)
	}
}
