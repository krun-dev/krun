package storage

import (
	"context"
	"testing"
	"time"

	"github.com/krun-dev/krun/internal/domain"
)

func TestPostgresConfigurationSnapshotsRoundTripCASAndRetention(t *testing.T) {
	s := postgresTestStore(t)
	ctx := context.Background()
	old := now().Add(-40 * 24 * time.Hour)
	secret := domain.WorkerSecretVersion{ID: "configuration-secret", Organization: "org-config", Worker: "worker-config", Environment: "production", Name: "TOKEN", ValueDigest: "digest", KeyVersion: "test", CreatedBy: "user", Created: old}
	c := domain.WorkerConfiguration{ID: "config-active", Organization: secret.Organization, Worker: secret.Worker, Environment: secret.Environment, Name: "XXX 测试", Message: "配置说明", Variables: map[string]any{"TEXT": "frozen"}, R2Bindings: map[string]domain.WorkerR2Binding{"UPLOADS": {BucketID: "config-bucket", BucketName: "uploads", StoreID: "default"}}, SecretVersions: map[string]string{"TOKEN": secret.ID}, SecretNames: []string{"TOKEN"}, Created: old}
	if err := s.Update(ctx, PartitionAll, func(st *domain.State) error {
		st.Organizations[c.Organization] = domain.Organization{ID: c.Organization, Name: "Configurations", Revision: 1}
		st.Resources[c.Worker] = domain.Resource{ID: c.Worker, Organization: c.Organization, Name: "Configurations", Kind: "worker", Revision: 1}
		st.Workers.Workers[c.Worker] = domain.Worker{Worker: c.Worker, Organization: c.Organization, Environments: map[string]domain.WorkerEnvironment{"production": {Name: "production", Enabled: true, ConfigRevision: 1, Variables: map[string]any{}}}}
		st.Workers.SecretVersions[secret.ID] = secret
		st.PendingWorkerSecrets = map[string]domain.EncryptedWorkerSecret{secret.ID: {Version: secret, Ciphertext: make([]byte, 32), Nonce: make([]byte, 12)}}
		st.Workers.Buckets["config-bucket"] = domain.WorkerBucket{ID: "config-bucket", Organization: c.Organization, Name: "uploads", Store: "default", Enabled: true, Revision: 1, Created: old}
		st.Workers.Configurations[c.ID] = c
		st.PendingWorkerConfigurationGuards = map[string]int{c.ID: 1}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.Open(WithReadScope(ctx, ReadScope{Organization: c.Organization, Worker: c.Worker, Kind: "worker", Limit: 100}), PartitionAll)
	if err != nil {
		t.Fatal(err)
	}
	got := loaded.Workers.Configurations[c.ID]
	if got.Name != c.Name || got.Message != c.Message || got.Variables["TEXT"] != "frozen" || got.SecretVersions["TOKEN"] != secret.ID || got.R2Bindings["UPLOADS"] != c.R2Bindings["UPLOADS"] {
		t.Fatalf("configuration or binding not reloaded: %+v", got)
	}
	if err := s.Update(ctx, PartitionWorkers, func(*domain.State) error { return nil }); err != nil {
		t.Fatalf("unchanged configuration rejected: %v", err)
	}
	if err := s.Update(ctx, PartitionWorkers, func(st *domain.State) error {
		c := st.Workers.Configurations["config-active"]
		c.Name = "mutated"
		st.Workers.Configurations[c.ID] = c
		return nil
	}); err == nil {
		t.Fatal("immutable configuration changed")
	}
	// A second replica changes the environment after the snapshot was loaded.
	err = s.Update(ctx, PartitionWorkers, func(st *domain.State) error {
		if _, err := s.pool.Exec(ctx, `UPDATE krun_worker_environments SET config_revision=2 WHERE worker_id=$1`, c.Worker); err != nil {
			return err
		}
		stale := c
		stale.ID = "config-stale"
		st.Workers.Configurations[stale.ID] = stale
		st.PendingWorkerConfigurationGuards = map[string]int{stale.ID: 1}
		return nil
	})
	if err == nil {
		t.Fatal("stale snapshot persisted")
	}
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM krun_worker_configurations WHERE configuration_id='config-stale'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("failed CAS left a row: %d %v", n, err)
	}
	if err := s.Update(ctx, PartitionWorkers, func(st *domain.State) error {
		expired := c
		expired.ID = "config-expired"
		st.Workers.Configurations[expired.ID] = expired
		st.PendingWorkerConfigurationGuards = map[string]int{expired.ID: 2}
		st.Workers.Artifacts["config-artifact"] = domain.WorkerArtifact{ID: "config-artifact", Organization: c.Organization, State: "finalized", Created: now()}
		st.Workers.Versions["config-release"] = domain.WorkerVersion{ID: "config-release", Organization: c.Organization, Worker: c.Worker, Environment: c.Environment, Artifact: "config-artifact", Configuration: c.ID, Created: now(), Variables: map[string]any{}}
		w := st.Workers.Workers[c.Worker]
		env := w.Environments[c.Environment]
		env.Version = "config-release"
		env.Confirmed = "config-release"
		w.Environments[c.Environment] = env
		st.Workers.Workers[c.Worker] = w
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.RetainExpired(ctx); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"krun_worker_configurations", "krun_worker_configuration_secrets", "krun_worker_configuration_r2"} {
		if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE configuration_id='config-expired'`).Scan(&n); err != nil || n != 0 {
			t.Fatalf("expired snapshot/binding retained in %s: %d %v", table, n, err)
		}
	}
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM krun_worker_configurations WHERE configuration_id=$1`, c.ID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("active configuration expired: %d %v", n, err)
	}
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM krun_worker_secret_versions WHERE secret_version_id=$1`, secret.ID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("saved configuration lost its secret: %d %v", n, err)
	}
}
