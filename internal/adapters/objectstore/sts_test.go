package objectstore

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/domain"
)

type sessionFunc func(context.Context, domain.StorageCredentialScope, time.Duration) (domain.StorageCredentials, error)

func (f sessionFunc) Credentials(ctx context.Context, scope domain.StorageCredentialScope, minimum time.Duration) (domain.StorageCredentials, error) {
	return f(ctx, scope, minimum)
}

func TestSTSS3ListsWithSessionAndSignsForFullBuildLifetime(t *testing.T) {
	const prefix = "company/test/"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("prefix") != prefix+"build-logs/" ||
			r.Header.Get("X-Amz-Security-Token") != "temp-token" ||
			!strings.Contains(r.Header.Get("Authorization"), "Credential=temp-access/") {
			t.Error("expected a prefix-scoped list signed with session credentials")
		}
		fmt.Fprint(w, `<ListBucketResult><IsTruncated>false</IsTruncated></ListBucketResult>`)
	}))
	defer server.Close()

	cfg := &config.WorkersConfig{ArtifactStorage: config.WorkerArtifactStorageConfig{
		Bucket: "artifacts", Prefix: strings.TrimSuffix(prefix, "/"), Region: "us-east-1", AddressingStyle: "path",
	}}
	scope := domain.StorageCredentialScope{Bucket: "artifacts", Prefix: prefix}
	const ttl = 35 * time.Minute
	reject := false
	source := sessionFunc(func(_ context.Context, got domain.StorageCredentialScope, minimum time.Duration) (domain.StorageCredentials, error) {
		if got != scope || (minimum != 30*time.Second && minimum != ttl+30*time.Second) {
			t.Errorf("unexpected credential scope or minimum validity: %v, %v", got, minimum)
		}
		if reject {
			return domain.StorageCredentials{}, errors.New("session denied")
		}
		return domain.StorageCredentials{
			AccessKeyID: "temp-access", SecretAccessKey: "temp-secret",
			SessionToken: "temp-token", ExpiresAt: time.Now().Add(time.Hour),
		}, nil
	})
	client, err := newSTSS3(cfg, server.URL, source, scope)
	if err != nil {
		t.Fatal(err)
	}
	for item := range client.ListObjects(t.Context(), "artifacts", ListObjectsOptions{Prefix: "build-logs/"}) {
		if item.Err != nil {
			t.Fatal(item.Err)
		}
	}
	signed, err := client.PresignedPutObject(t.Context(), "artifacts", "uploads/example.zip", ttl)
	if err != nil {
		t.Fatal(err)
	}
	if signed.Path != "/artifacts/"+prefix+"uploads/example.zip" ||
		signed.Query().Get("X-Amz-Security-Token") != "temp-token" ||
		signed.Query().Get("X-Amz-Expires") != "2100" {
		t.Fatal("build URL is missing the prefix, session token or requested lifetime")
	}
	reject = true
	if _, err := client.PresignedGetObject(t.Context(), "artifacts", "uploads/example.zip", ttl, nil); err == nil {
		t.Fatal("presigning unexpectedly succeeded after the source rejected the request")
	}
}
