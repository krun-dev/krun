package storagecredentials

import (
	"context"
	"testing"
	"time"

	"github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/domain"
)

func TestRegistryPinsBucketAndNamespaceAndDoesNotMutateConfig(t *testing.T) {
	c := &config.WorkerObjectStorageConfig{
		DefaultStore: "primary",
		Stores: []config.WorkerObjectStore{{
			ID: "primary", Endpoint: "http://127.0.0.1:9000", Region: "us-east-1", Bucket: "example-bucket",
			RootPrefix: "worker-data", AddressingStyle: "path",
			Credentials: config.WorkerStorageCredentialConfig{Type: "static", AccessKeyID: "example-access", SecretAccessKey: "example-secret"},
		}},
	}
	r, err := NewRegistry(c)
	if err != nil {
		t.Fatal(err)
	}
	if c.CredentialCacheEntries != 0 {
		t.Fatal("factory mutated application configuration")
	}
	if _, err := r.Credentials(context.Background(), testScope(), time.Minute); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*domain.StorageCredentialScope){
		func(s *domain.StorageCredentialScope) { s.StoreID = "other" },
		func(s *domain.StorageCredentialScope) { s.Bucket = "other-bucket" },
		func(s *domain.StorageCredentialScope) { s.Prefix = "worker-data-adjacent/" },
		func(s *domain.StorageCredentialScope) { s.Prefix = "artifacts/" },
	} {
		s := testScope()
		mutate(&s)
		if _, err := r.Credentials(context.Background(), s, time.Minute); err == nil {
			t.Fatal("accepted scope outside configured store")
		}
	}
}
