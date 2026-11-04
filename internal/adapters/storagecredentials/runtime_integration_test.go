package storagecredentials

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/securetoken"
	"github.com/minio/minio-go/v7"
)

// Runs the actual shipped workerd entrypoint and facade against configured
// storage. The temporary credential file is private and removed by testing.
func TestConfiguredR2Runtime(t *testing.T) {
	if *integrationConfig == "" {
		t.Skip("provide -storage-config for the real workerd/storage test")
	}
	cfg, err := config.LoadConfig(*integrationConfig)
	if err != nil || cfg.Workers == nil || cfg.Workers.ObjectStorage == nil {
		t.Fatal("main object storage configuration is required")
	}
	registry, err := NewRegistry(cfg.Workers.ObjectStorage)
	if err != nil {
		t.Fatal(err)
	}
	for _, store := range cfg.Workers.ObjectStorage.Stores {
		t.Run(store.ID, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			id := securetoken.NewID("runtime")
			prefix := store.RootPrefix + "/integration-tests/" + id + "/"
			scope := domain.StorageCredentialScope{Organization: "integration-test", BucketID: id, StoreID: store.ID, Bucket: store.Bucket, Prefix: prefix, Access: domain.StorageAllAccess, AuthorizationRevision: 1}
			credentials, err := registry.Credentials(ctx, scope, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			client := integrationClient(t, store, credentials)
			privateDirectory := t.TempDir()
			t.Cleanup(func() {
				cleanup, stop := context.WithTimeout(context.Background(), 20*time.Second)
				defer stop()
				cleanupProjectUploads(t, cleanup, client, store.Bucket, prefix, filepath.Join(privateDirectory, "multipart.jsonl"))
				if err := client.RemoveObject(cleanup, store.Bucket, prefix+"hello.txt", minio.RemoveObjectOptions{}); err != nil {
					t.Error("runtime fixture cleanup failed")
				}
				for _, key := range []string{"encoded.txt", "page-a", "page-b", "page-c"} {
					_ = client.RemoveObject(cleanup, store.Bucket, prefix+key, minio.RemoveObjectOptions{})
					_, err := client.StatObject(cleanup, store.Bucket, prefix+key, minio.StatObjectOptions{})
					if minio.ToErrorResponse(err).StatusCode != 404 {
						t.Error("runtime fixture absence could not be confirmed")
					}
				}

				_, err := client.StatObject(cleanup, store.Bucket, prefix+"hello.txt", minio.StatObjectOptions{})
				if minio.ToErrorResponse(err).StatusCode != 404 {
					t.Error("runtime fixture absence could not be confirmed")
				}
			})
			var expiry any
			if !credentials.ExpiresAt.IsZero() {
				expiry = credentials.ExpiresAt
			}
			raw, err := json.Marshal(map[string]any{
				"endpoint": store.Endpoint, "region": store.Region, "bucket": store.Bucket, "prefix": prefix, "addressing_style": store.AddressingStyle,
				"access_key_id": credentials.AccessKeyID, "secret_access_key": credentials.SecretAccessKey, "session_token": credentials.SessionToken,
				"expires_at": expiry, "lease_until": time.Now().Add(5 * time.Minute), "cursor_key": securetoken.Random(),
			})
			if err != nil {
				t.Fatal("grant encoding failed")
			}
			filename := filepath.Join(privateDirectory, "grant.json")
			if err := os.WriteFile(filename, raw, 0600); err != nil {
				t.Fatal("grant file creation failed")
			}
			script := filepath.Join("..", "..", "..", "workers", "runtime", "test", "r2-workerd-smoke.mjs")
			command := exec.CommandContext(ctx, "node", script, "--grant-file="+filename)
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("real R2 runtime test failed: %s", output)
			}
			t.Log(string(output))
		})
	}
}
