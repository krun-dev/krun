package bucketobjects

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/domain"
)

type credentialFixture struct {
	scope domain.StorageCredentialScope
	grant domain.StorageCredentials
	calls int
}

func (f *credentialFixture) Credentials(_ context.Context, scope domain.StorageCredentialScope, _ time.Duration) (domain.StorageCredentials, error) {
	f.scope = scope
	f.calls++
	return f.grant, nil
}

func TestAccessScopeExpirationAndR2Metadata(t *testing.T) {
	source := &credentialFixture{grant: domain.StorageCredentials{
		AccessKeyID: "fixture-ak", SecretAccessKey: "fixture-sk", SessionToken: "fixture-token",
		ExpiresAt: time.Now().UTC().Add(125 * time.Second),
	}}
	client := New(&config.WorkerObjectStorageConfig{
		CursorKey: strings.Repeat("A", 43),
		Stores:    []config.WorkerObjectStore{{ID: "fixture", Endpoint: "https://storage.example.com", Region: "us-east-1", Bucket: "physical-bucket", RootPrefix: "data/v1", AddressingStyle: "path"}},
	}, source)
	bucket := domain.WorkerBucket{ID: "bkt-one", Organization: "org-one", Store: "fixture", Enabled: true, Revision: 7}
	result, err := client.Access(context.Background(), bucket, domain.BucketObjectAccessInput{
		Key: "folder/你好.txt", Operation: "upload", Size: 12,
		HTTPMetadata: map[string]string{"contentType": "text/plain"}, CustomMetadata: map[string]string{"中文": "保留大小写"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ExpiresAt.After(source.grant.ExpiresAt.Add(-30*time.Second)) || time.Until(result.ExpiresAt) < time.Minute {
		t.Fatal("capability outlived its STS safety window")
	}
	if source.scope.Access != domain.StorageWrite || source.scope.AuthorizationRevision != 7 || source.scope.Prefix != "data/v1/org/org-one/buckets/bkt-one/" {
		t.Fatal("credentials were not limited to the authorized operation and bucket")
	}
	parsed, _ := url.Parse(result.URL)
	if parsed.Path != "/physical-bucket/data/v1/org/org-one/buckets/bkt-one/folder/你好.txt" || parsed.Query().Get("X-Amz-Security-Token") != "fixture-token" {
		t.Fatal("capability lost its logical mapping or STS token")
	}
	encoded, err := base64.StdEncoding.DecodeString(result.Headers["X-Amz-Meta-Krun-V1"])
	var metadata envelope
	if err != nil || json.Unmarshal(encoded, &metadata) != nil || metadata.Version != result.Version || metadata.CustomMetadata["中文"] != "保留大小写" {
		t.Fatal("R2 envelope changed custom metadata or version")
	}
	for _, input := range []domain.BucketObjectAccessInput{
		{Key: "../escape", Operation: "upload"},
		{Key: "folder/../escape", Operation: "read"},
		{Key: "folder\\escape", Operation: "read"},
		{Key: "ok", Operation: "upload", Size: -1},
		{Key: "ok", Operation: "upload", Size: 5<<30 + 1},
		{Key: "ok", Operation: "upload", HTTPMetadata: map[string]string{"contentType": "text/plain\r\nX-Forged: true"}},
		{Key: "ok", Operation: "read", CustomMetadata: map[string]string{"x": "y"}},
	} {
		before := source.calls
		if _, err := client.Access(context.Background(), bucket, input); err == nil || source.calls != before {
			t.Fatal("invalid capability request reached the credential provider")
		}
	}
	source.grant.ExpiresAt = time.Now().Add(45 * time.Second)
	if _, err := client.Access(context.Background(), bucket, domain.BucketObjectAccessInput{Key: "ok", Operation: "read"}); err == nil {
		t.Fatal("nearly expired credentials issued a URL")
	}
}
