package bucketobjects

import (
	"bytes"
	"context"
	"flag"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/krun-dev/krun/internal/adapters/storagecredentials"
	"github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/securetoken"
)

var integrationConfig = flag.String("storage-config", "", "Main YAML for disposable direct object-access integration tests")

func TestConfiguredDirectAccess(t *testing.T) {
	if *integrationConfig == "" {
		t.Skip("provide -storage-config to test real object storage")
	}
	cfg, err := config.LoadConfig(*integrationConfig)
	if err != nil || cfg.Workers == nil || cfg.Workers.ObjectStorage == nil {
		t.Fatal("main object-storage configuration could not be loaded")
	}
	registry, err := storagecredentials.NewRegistry(cfg.Workers.ObjectStorage)
	if err != nil {
		t.Fatal("credential registry could not be initialized")
	}
	client := New(cfg.Workers.ObjectStorage, registry)
	for _, store := range cfg.Workers.ObjectStorage.Stores {
		t.Run(store.ID, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			bucket := domain.WorkerBucket{ID: securetoken.NewID("probe"), Organization: "integration-test", Store: store.ID, Enabled: true, Revision: 1}
			keys := []string{"目录/a +?#%.txt", "目录/b.txt", "目录/sub/c.txt"}
			t.Cleanup(func() {
				cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
				defer stop()
				for _, key := range keys {
					if err := client.Delete(cleanup, bucket, key); err != nil {
						t.Error("disposable object cleanup failed")
					}
				}
			})
			data := []byte("direct upload fixture\n")
			httpClient := &http.Client{Timeout: 30 * time.Second}
			for _, key := range keys {
				access, err := client.Access(ctx, bucket, domain.BucketObjectAccessInput{
					Key: key, Operation: "upload", Size: int64(len(data)),
					HTTPMetadata: map[string]string{"contentType": "text/plain"}, CustomMetadata: map[string]string{"test": "direct"},
				})
				if err != nil {
					t.Fatal("upload signing failed", err)
				}
				parsed, _ := url.Parse(access.URL)
				if !strings.Contains(parsed.Query().Get("X-Amz-SignedHeaders"), "x-amz-meta-krun-v1") {
					t.Fatal("R2 metadata is not protected by the signature")
				}
				req, _ := http.NewRequestWithContext(ctx, access.Method, access.URL, bytes.NewReader(data))
				for name, value := range access.Headers {
					req.Header.Set(name, value)
				}
				response, err := httpClient.Do(req)
				if err != nil {
					t.Fatal("direct upload network failure")
				}
				_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
				response.Body.Close()
				if response.StatusCode != 200 {
					t.Fatalf("direct upload status %d", response.StatusCode)
				}
				info, err := client.Head(ctx, bucket, key)
				if err != nil || info.R2Compatible == nil || !*info.R2Compatible || info.Version != access.Version || info.CustomMetadata["test"] != "direct" || info.Size != int64(len(data)) {
					t.Fatal("direct upload did not preserve the R2 metadata profile", err)
				}
			}
			first, err := client.List(ctx, bucket, domain.BucketObjectList{Prefix: "目录/", Limit: 1, Delimiter: "/"})
			if err != nil || !first.Truncated || first.Cursor == "" || len(first.Objects)+len(first.Prefixes) != 1 {
				t.Fatal("bounded directory listing failed", err)
			}
			if _, err := client.List(ctx, bucket, domain.BucketObjectList{Prefix: "other/", Limit: 1, Cursor: first.Cursor, Delimiter: "/"}); err == nil {
				t.Fatal("cursor crossed directory scope")
			}
			next, err := client.List(ctx, bucket, domain.BucketObjectList{Prefix: "目录/", Limit: 1, Cursor: first.Cursor, Delimiter: "/"})
			if err != nil || len(next.Objects)+len(next.Prefixes) != 1 {
				t.Fatal("continuation failed", err)
			}
			last, err := client.List(ctx, bucket, domain.BucketObjectList{Prefix: "目录/", Limit: 1, Cursor: next.Cursor, Delimiter: "/"})
			if err != nil || last.Truncated || len(last.Prefixes) != 1 || last.Prefixes[0] != "目录/sub/" {
				t.Fatal("directory delimiter pagination failed", err)
			}
			access, err := client.Access(ctx, bucket, domain.BucketObjectAccessInput{Key: keys[0], Operation: "download"})
			if err != nil {
				t.Fatal("download signing failed", err)
			}
			response, err := httpClient.Get(access.URL)
			if err != nil {
				t.Fatal("direct download network failure")
			}
			got, err := io.ReadAll(io.LimitReader(response.Body, 1024))
			response.Body.Close()
			if err != nil || response.StatusCode != 200 || !bytes.Equal(got, data) || !strings.Contains(response.Header.Get("Content-Disposition"), "attachment") {
				t.Fatal("direct download failed")
			}
			other := bucket
			other.ID = securetoken.NewID("probe")
			if _, err := client.Head(ctx, other, keys[0]); err == nil {
				t.Fatal("logical bucket isolation failed")
			}
		})
	}
}
