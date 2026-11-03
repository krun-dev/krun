package objectstore

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/krun-dev/krun/internal/config"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

func TestPrefixClientAllOperations(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen[r.Method+" "+r.URL.Path] = true
		mu.Unlock()
		if r.Method == "GET" && r.URL.Path == "/bucket/" {
			if r.URL.Query().Get("prefix") != "dev/run/uploads/" || r.URL.Query().Get("start-after") != "dev/run/uploads/old" {
				t.Errorf("unscoped listing: %s", r.URL.RawQuery)
			}
			w.Header().Set("Content-Type", "application/xml")
			io.WriteString(w, `<ListBucketResult><Name>bucket</Name><Prefix>dev/run/uploads/</Prefix><IsTruncated>false</IsTruncated><Contents><Key>dev/run/uploads/a</Key><Size>4</Size></Contents><Contents><Key>another-app/file</Key><Size>4</Size></Contents></ListBucketResult>`)
			return
		}
		if r.Method == "POST" {
			raw, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(raw), "dev/run/uploads/a") {
				t.Errorf("unscoped delete: %s", raw)
			}
			w.Header().Set("Content-Type", "application/xml")
			io.WriteString(w, `<DeleteResult><Deleted><Key>dev/run/uploads/a</Key></Deleted></DeleteResult>`)
			return
		}
		w.Header().Set("Last-Modified", "Tue, 03 Nov 2026 01:02:03 GMT")
		w.Header().Set("ETag", `"fixture"`)
		w.Header().Set("Content-Length", "4")
		w.Header().Set("Content-Type", "text/plain")
		if r.Method == "DELETE" {
			w.Header().Del("Content-Length")
			w.WriteHeader(204)
			return
		}
		if r.Method == "PUT" {
			io.Copy(io.Discard, r.Body)
			w.Header().Set("Content-Length", "0")
			return
		}
		if r.Method == "GET" {
			io.WriteString(w, "data")
		}
	}))
	defer server.Close()
	endpoint, _ := url.Parse(server.URL)
	raw, err := minio.New(endpoint.Host, &minio.Options{
		Creds:        credentials.NewStaticV4("test-access", "test-secret", ""),
		Region:       "cn-beijing",
		BucketLookup: minio.BucketLookupPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	client := &prefixedClient{Client: raw, prefix: "dev/run/"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err = client.PutObject(ctx, "bucket", "uploads/a", strings.NewReader("data"), 4, PutObjectOptions{
		DisableMultipart: true,
	}); err != nil {
		t.Fatal(err)
	}
	obj, err := client.GetObject(ctx, "bucket", "uploads/a", GetObjectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(obj)
	obj.Close()
	if err != nil || string(data) != "data" {
		t.Fatal(string(data), err)
	}
	if _, err = client.StatObject(ctx, "bucket", "uploads/a", StatObjectOptions{}); err != nil {
		t.Fatal(err)
	}
	listed := []string{}
	for object := range client.ListObjects(ctx, "bucket", ListObjectsOptions{
		Prefix:     "uploads/",
		StartAfter: "uploads/old",
		Recursive:  true,
	}) {
		if object.Err != nil {
			t.Fatal(object.Err)
		}
		listed = append(listed, object.Key)
	}
	if len(listed) != 1 || listed[0] != "uploads/a" {
		t.Fatal(listed)
	}
	objects := make(chan ObjectInfo, 1)
	objects <- ObjectInfo{Key: "uploads/a"}
	close(objects)
	for item := range client.RemoveObjects(ctx, "bucket", objects, RemoveObjectsOptions{}) {
		t.Fatal(item)
	}
	if err = client.RemoveObject(ctx, "bucket", "uploads/a", RemoveObjectOptions{}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, method := range []string{"GET", "HEAD", "PUT", "DELETE"} {
		if !seen[method+" /bucket/dev/run/uploads/a"] {
			t.Errorf("missing prefixed %s", method)
		}
	}
}
func TestVirtualHostPresigningAndRegion(t *testing.T) {
	cfg := &config.WorkersConfig{
		S3Endpoint:        "https://storage.example.com",
		S3AddressingStyle: "virtual",
		S3Prefix:          "dev/run",
		S3Region:          "cn-beijing",
		S3AccessKey:       "test-access",
		S3SecretKey:       "test-secret",
	}
	client, err := NewWorkerS3(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, put := range []bool{false, true} {
		var signed *url.URL
		if put {
			signed, err = client.PresignedPutObject(context.Background(), "bucket", "v1/test", time.Minute)
		} else {
			signed, err = client.PresignedGetObject(context.Background(), "bucket", "v1/test", time.Minute, nil)
		}
		if err != nil {
			t.Fatal(err)
		}
		if signed.Host != "bucket.storage.example.com" || signed.Path != "/dev/run/v1/test" || !strings.Contains(signed.Query().Get("X-Amz-Credential"), "/cn-beijing/s3/") {
			t.Fatal("incorrect host, prefix, or signing region")
		}
	}
}

func TestPrefixRejectsEscapingKeysBeforeNetworkAccess(t *testing.T) {
	// A nil underlying client makes any accidental network dispatch fail the test.
	client := &prefixedClient{prefix: "dev/run/"}
	for _, key := range []string{"../other", "a/../../other", "/root", "a//b", "a\\b", "a/../b"} {
		if err := client.RemoveObject(context.Background(), "bucket", key, RemoveObjectOptions{}); err == nil {
			t.Fatal("accepted escaping key")
		}
		if _, err := client.PresignedGetObject(context.Background(), "bucket", key, time.Minute, nil); err == nil {
			t.Fatal("signed escaping key")
		}
	}
}
