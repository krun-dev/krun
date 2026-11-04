package storagecredentials

import (
	"bytes"
	"context"
	"flag"
	"io"
	"net/url"
	"testing"
	"time"

	"github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/securetoken"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

var integrationConfig = flag.String("storage-config", "", "Explicit main YAML for disposable object-storage integration tests")

// Explicit opt-in: normal test runs never use developer credentials or mutate
// a real bucket. Only randomly named fixtures below the configured root are used.
func TestConfiguredStorage(t *testing.T) {
	if *integrationConfig == "" {
		t.Skip("provide -storage-config pointing to the main YAML to test real object storage")
	}
	cfg, err := config.LoadConfig(*integrationConfig)
	if err != nil {
		t.Fatal("main configuration could not be loaded")
	}
	if cfg.Workers == nil || cfg.Workers.ObjectStorage == nil {
		t.Fatal("workers.object_storage is required")
	}
	r, err := NewRegistry(cfg.Workers.ObjectStorage)
	if err != nil {
		t.Fatal(err)
	}
	for _, store := range cfg.Workers.ObjectStorage.Stores {
		t.Run(store.ID, func(t *testing.T) { verifyConfiguredStore(t, r, store) })
	}
}

func verifyConfiguredStore(t *testing.T, registry *Registry, store config.WorkerObjectStore) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	id := securetoken.NewID("probe")
	base := domain.StorageCredentialScope{
		Organization: "integration-test", BucketID: id, StoreID: store.ID, Bucket: store.Bucket,
		Prefix:                store.RootPrefix + "/integration-tests/" + id + "/allowed/",
		Access:                domain.StorageRead | domain.StorageWrite | domain.StorageDelete | domain.StorageList,
		AuthorizationRevision: 1,
	}
	outside := base
	outside.Prefix = store.RootPrefix + "/integration-tests/" + id + "/outside/"
	insideCredentials, err := registry.Credentials(ctx, base, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	cached, err := registry.Credentials(ctx, base, time.Minute)
	if err != nil || cached != insideCredentials {
		t.Fatal("credential cache did not reuse the issued session")
	}
	outsideCredentials, err := registry.Credentials(ctx, outside, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	client := integrationClient(t, store, insideCredentials)
	control := integrationClient(t, store, outsideCredentials)
	insideKey := base.Prefix + "hello.txt"
	outsideKey := outside.Prefix + "sentinel.txt"
	blockedKey := outside.Prefix + "blocked.txt"
	t.Cleanup(func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		for _, fixture := range []struct {
			client *minio.Client
			key    string
		}{{client, insideKey}, {control, outsideKey}, {control, blockedKey}} {
			if err := fixture.client.RemoveObject(cleanupCtx, store.Bucket, fixture.key, minio.RemoveObjectOptions{}); err != nil {
				t.Error("fixture cleanup failed:", minio.ToErrorResponse(err).Code)
				continue
			}
			_, err := fixture.client.StatObject(cleanupCtx, store.Bucket, fixture.key, minio.StatObjectOptions{})
			if minio.ToErrorResponse(err).StatusCode != 404 {
				t.Error("fixture absence could not be confirmed")
			}
		}
	})
	body := []byte("krun storage integration fixture\n")
	put := func(c *minio.Client, key string) error {
		_, err := c.PutObject(ctx, store.Bucket, key, bytes.NewReader(body), int64(len(body)), minio.PutObjectOptions{ContentType: "text/plain", DisableMultipart: true})
		return err
	}
	get := func(c *minio.Client, key string) ([]byte, error) {
		object, err := c.GetObject(ctx, store.Bucket, key, minio.GetObjectOptions{})
		if err != nil {
			return nil, err
		}
		defer object.Close()
		return io.ReadAll(io.LimitReader(object, 1024))
	}
	listOne := func(c *minio.Client, prefix string) (string, error) {
		listCtx, stop := context.WithCancel(ctx)
		defer stop()
		for item := range c.ListObjects(listCtx, store.Bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true, MaxKeys: 1}) {
			return item.Key, item.Err
		}
		return "", nil
	}
	if err := put(control, outsideKey); err != nil {
		t.Fatal("outside fixture creation failed:", minio.ToErrorResponse(err).Code)
	}
	if err := put(client, insideKey); err != nil {
		t.Fatal("scoped PUT failed:", minio.ToErrorResponse(err).Code)
	}
	read, err := get(client, insideKey)
	if err != nil || !bytes.Equal(read, body) {
		t.Fatal("GET content mismatch")
	}
	info, err := client.StatObject(ctx, store.Bucket, insideKey, minio.StatObjectOptions{})
	if err != nil || info.Size != int64(len(body)) {
		t.Fatal("HEAD failed")
	}
	key, err := listOne(client, base.Prefix)
	if err != nil || key != insideKey {
		t.Fatal("scoped LIST failed")
	}
	if registry.stores[store.ID].manager.Capabilities().CanRestrictScope {
		assertDenied := func(name string, err error) {
			t.Helper()
			response := minio.ToErrorResponse(err)
			if response.Code != "AccessDenied" || response.StatusCode != 403 {
				t.Errorf("%s: expected AccessDenied 403, got %s %d", name, response.Code, response.StatusCode)
			}
		}
		_, err = get(client, outsideKey)
		assertDenied("outside GET", err)
		assertDenied("outside PUT", put(client, blockedKey))
		assertDenied("outside DELETE", client.RemoveObject(ctx, store.Bucket, outsideKey, minio.RemoveObjectOptions{}))
		_, err = listOne(client, outside.Prefix)
		assertDenied("outside LIST", err)
		_, err = listOne(client, "")
		assertDenied("bucket-wide LIST", err)
		read, err = get(control, outsideKey)
		if err != nil || !bytes.Equal(read, body) {
			t.Fatal("outside fixture was changed")
		}
	} else {
		t.Log("static provider: cloud-side prefix isolation is not available")
	}
	if err := client.RemoveObject(ctx, store.Bucket, insideKey, minio.RemoveObjectOptions{}); err != nil {
		t.Fatal("scoped DELETE failed")
	}
	_, err = client.StatObject(ctx, store.Bucket, insideKey, minio.StatObjectOptions{})
	if minio.ToErrorResponse(err).StatusCode != 404 {
		t.Fatal("deleted fixture still present")
	}
}

func integrationClient(t *testing.T, store config.WorkerObjectStore, c domain.StorageCredentials) *minio.Client {
	t.Helper()
	u, err := url.Parse(store.Endpoint)
	if err != nil {
		t.Fatal("invalid storage endpoint")
	}
	lookup := minio.BucketLookupPath
	if store.AddressingStyle == "virtual" {
		lookup = minio.BucketLookupDNS
	}
	client, err := minio.New(u.Host, &minio.Options{
		Creds:  credentials.NewStaticV4(c.AccessKeyID, c.SecretAccessKey, c.SessionToken),
		Secure: u.Scheme == "https", Region: store.Region, BucketLookup: lookup,
	})
	if err != nil {
		t.Fatal("S3 client construction failed")
	}
	return client
}
