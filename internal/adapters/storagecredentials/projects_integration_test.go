package storagecredentials

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/securetoken"
	"github.com/minio/minio-go/v7"
)

var integrationProjects = flag.String("storage-projects", "", "Directory of pinned upstream repositories for the opt-in R2 compatibility test")

func TestConfiguredR2Projects(t *testing.T) {
	if *integrationConfig == "" || *integrationProjects == "" {
		t.Skip("provide -storage-config and -storage-projects for upstream compatibility tests")
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
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			id := securetoken.NewID("projects")
			prefix := store.RootPrefix + "/integration-tests/" + id + "/"
			scope := domain.StorageCredentialScope{
				Organization:          "integration-test",
				BucketID:              id,
				StoreID:               store.ID,
				Bucket:                store.Bucket,
				Prefix:                prefix,
				Access:                domain.StorageAllAccess,
				AuthorizationRevision: 1,
			}
			credentials, err := registry.Credentials(ctx, scope, 3*time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			client := integrationClient(t, store, credentials)
			privateDirectory := t.TempDir()
			t.Cleanup(func() {
				cleanup, stop := context.WithTimeout(context.Background(), 45*time.Second)
				defer stop()
				cleanupProjectUploads(t, cleanup, client, store.Bucket, prefix, filepath.Join(privateDirectory, "multipart.jsonl"))
				count := 0
				for object := range client.ListObjects(cleanup, store.Bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true}) {
					if object.Err != nil {
						t.Error("project fixture listing failed")
						return
					}
					count++
					if count > 200 || !strings.HasPrefix(object.Key, prefix) {
						t.Error("project fixture cleanup exceeded its scope")
						return
					}
					if err := client.RemoveObject(cleanup, store.Bucket, object.Key, minio.RemoveObjectOptions{}); err != nil {
						t.Error("project fixture cleanup failed")
					}
				}
				for object := range client.ListObjects(cleanup, store.Bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true}) {
					if object.Err != nil || object.Key != "" {
						t.Error("project fixture cleanup could not be confirmed")
					}
				}
			})

			// This read-only upstream project expects pre-existing objects. Seed
			// controlled fixture metadata; other projects upload through the facade.
			objects := map[string]string{
				"share.txt": "upstream R2 compatibility: 中文\n",
				"media.mp4": "0123456789abcdef",
			}
			for index := 0; index < 65; index++ {
				objects[fmt.Sprintf("page-%03d.txt", index)] = "page"
			}
			for key, value := range objects {
				envelope, err := json.Marshal(map[string]any{
					"version":        securetoken.NewID("fixture"),
					"customMetadata": map[string]string{},
					"httpMetadata":   map[string]string{},
				})
				if err != nil {
					t.Fatal(err)
				}
				_, err = client.PutObject(ctx, store.Bucket, prefix+key, strings.NewReader(value), int64(len(value)), minio.PutObjectOptions{
					UserMetadata: map[string]string{"krun-v1": base64.StdEncoding.EncodeToString(envelope)},
				})
				if err != nil {
					t.Fatal("project fixture creation failed")
				}
			}
			var expiry any
			if !credentials.ExpiresAt.IsZero() {
				expiry = credentials.ExpiresAt
			}
			raw, err := json.Marshal(map[string]any{
				"endpoint":          store.Endpoint,
				"region":            store.Region,
				"bucket":            store.Bucket,
				"prefix":            prefix,
				"addressing_style":  store.AddressingStyle,
				"access_key_id":     credentials.AccessKeyID,
				"secret_access_key": credentials.SecretAccessKey,
				"session_token":     credentials.SessionToken,
				"expires_at":        expiry,
				"lease_until":       time.Now().Add(5 * time.Minute),
				"cursor_key":        securetoken.Random(),
			})
			if err != nil {
				t.Fatal("project grant encoding failed")
			}
			filename := filepath.Join(privateDirectory, "grant.json")
			if err := os.WriteFile(filename, raw, 0600); err != nil {
				t.Fatal("project grant creation failed")
			}
			script := filepath.Join("..", "..", "..", "workers", "runtime", "test", "r2-projects.mjs")
			command := exec.CommandContext(ctx, "node", script, "--grant-file="+filename, "--repos="+*integrationProjects)
			output, err := command.CombinedOutput()
			t.Log(string(output))
			if err != nil {
				t.Fatal("upstream R2 compatibility test failed")
			}
		})
	}
}

func cleanupProjectUploads(t *testing.T, ctx context.Context, client *minio.Client, bucket, prefix, filename string) {
	t.Helper()
	raw, err := os.ReadFile(filename)
	if os.IsNotExist(err) {
		return
	}
	if err != nil || len(raw) > 32768 {
		t.Error("invalid multipart cleanup journal")
		return
	}
	core := minio.Core{Client: client}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var record struct {
			Key   string `json:"key"`
			Token string `json:"token"`
		}
		if json.Unmarshal([]byte(line), &record) != nil || !strings.HasPrefix(record.Key, "explorer/") || strings.Contains(record.Key, "..") {
			t.Error("multipart cleanup journal is out of scope")
			continue
		}
		payload, err := base64.RawURLEncoding.DecodeString(strings.Split(record.Token, ".")[0])
		var identity struct {
			UploadID string `json:"uploadId"`
		}
		if err != nil || json.Unmarshal(payload, &identity) != nil || identity.UploadID == "" {
			t.Error("invalid multipart cleanup identity")
			continue
		}
		err = core.AbortMultipartUpload(ctx, bucket, prefix+record.Key, identity.UploadID)
		if err != nil && minio.ToErrorResponse(err).StatusCode != 404 {
			t.Error("multipart fixture cleanup failed")
		}
		_, err = core.ListObjectParts(ctx, bucket, prefix+record.Key, identity.UploadID, 0, 1)
		if minio.ToErrorResponse(err).StatusCode != 404 {
			t.Error("multipart fixture cleanup could not be confirmed")
		}
	}
}
