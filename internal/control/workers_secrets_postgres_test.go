package control

import (
	"bytes"
	"context"
	"testing"

	workerapp "github.com/krun-dev/krun/internal/workers"
)

func TestWorkerSecretsPersistEncryptedAndRelational(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	config := testWorkersConfig()
	enableWorkerSecrets(config)
	encrypted, err := workerapp.EncryptWorkerSecret(config, "org-secret", "svc-secret", "production", "API_TOKEN", "admin", "database-must-not-see-plaintext")
	if err != nil {
		t.Fatal(err)
	}
	refs := map[string]string{"API_TOKEN": encrypted.Version.ID}
	version := WorkerVersion{
		ID: "rel-secret-postgres", Organization: "org-secret", Worker: "svc-secret", Environment: "production",
		Artifact: "art-secret-postgres", ArchiveDigest: "archive", ModuleDigest: "module",
		ConfigRevision: 2, Variables: map[string]any{}, SecretDigest: workerapp.WorkerSecretsDigest(refs),
		SecretVersions: refs, Created: now(),
	}
	if err = store.Update(ctx, PartitionWorkers, func(st *State) error {
		st.Workers.SecretVersions[encrypted.Version.ID] = encrypted.Version
		st.PendingWorkerSecrets = map[string]encryptedWorkerSecret{encrypted.Version.ID: encrypted}
		st.Workers.Workers[version.Worker] = Worker{
			Worker: version.Worker, Organization: version.Organization,
			Environments: map[string]WorkerEnvironment{
				"production": {Name: "production", Enabled: true, ConfigRevision: 2, Variables: map[string]any{}, SecretNames: []string{"API_TOKEN"}, SecretVersions: refs},
			},
		}
		placed := now()
		st.Workers.Placements[workerPlacementKey(version.Worker, "default")] = WorkerPlacement{Worker: version.Worker, Organization: version.Organization, NetworkProfile: "default", Pool: "default", Role: "primary", State: "active", Revision: 1, Created: placed, Updated: placed}
		st.Workers.Artifacts[version.Artifact] = WorkerArtifact{ID: version.Artifact, Organization: version.Organization, State: "finalized", ArchiveDigest: version.ArchiveDigest, ModuleDigest: version.ModuleDigest, Created: now()}
		st.Workers.Versions[version.ID] = version
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	var ciphertext []byte
	if err = postgresTestPool(store).QueryRow(ctx, `SELECT ciphertext FROM krun_worker_secret_versions WHERE secret_version_id=$1`, encrypted.Version.ID).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ciphertext, []byte("database-must-not-see-plaintext")) {
		t.Fatal("plaintext persisted in PostgreSQL")
	}
	loaded, err := store.Open(ctx, PartitionWorkers)
	if err != nil {
		t.Fatal(err)
	}
	environment := loaded.Workers.Workers[version.Worker].Environments["production"]
	if environment.SecretVersions["API_TOKEN"] != encrypted.Version.ID || loaded.Workers.Versions[version.ID].SecretVersions["API_TOKEN"] != encrypted.Version.ID {
		t.Fatalf("relational bindings not reloaded: env=%v version=%v", environment.SecretVersions, loaded.Workers.Versions[version.ID].SecretVersions)
	}
	var topologyBefore, topologyAfter string
	if err = postgresTestPool(store).QueryRow(ctx, `SELECT COALESCE(max(generation),0)::text FROM worker_config_dirty`).Scan(&topologyBefore); err != nil {
		t.Fatal(err)
	}
	// PostgreSQL and the JSON snapshot clone may represent the same instant with
	// different time.Location pointers. An unrelated Workers write must not
	// mistake that representation difference for either a secret metadata or
	// runtime-placement mutation.
	if err = store.Update(ctx, PartitionWorkers, func(*State) error { return nil }); err != nil {
		t.Fatalf("unchanged secret metadata rejected: %v", err)
	}
	if err = postgresTestPool(store).QueryRow(ctx, `SELECT COALESCE(max(generation),0)::text FROM worker_config_dirty`).Scan(&topologyAfter); err != nil {
		t.Fatal(err)
	}
	if topologyAfter != topologyBefore {
		t.Fatalf("unchanged placement advanced topology generation: before=%s after=%s", topologyBefore, topologyAfter)
	}
	digest, values, err := store.EncryptedVersionSecrets(ctx, version.ID, "default")
	if err != nil || digest != version.SecretDigest || len(values) != 1 {
		t.Fatalf("runtime lookup: digest=%q values=%d err=%v", digest, len(values), err)
	}
	plain, err := workerapp.DecryptWorkerSecret(config, values[0])
	if err != nil || plain != "database-must-not-see-plaintext" {
		t.Fatalf("decrypt persisted secret: %q %v", plain, err)
	}
}
