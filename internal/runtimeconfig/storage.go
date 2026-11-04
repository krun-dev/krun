package runtimeconfig

import (
	"context"
	"time"

	"github.com/krun-dev/krun/internal/domain"
)

type StorageGrant struct {
	RequestTimeoutMilliseconds int
	CursorKey                  string
	BucketID                   string
	StoreID                    string
	Endpoint                   string
	Region                     string
	Bucket                     string
	Prefix                     string
	AddressingStyle            string
	AuthorizationRevision      int64
	LeaseUntil                 time.Time
	Credentials                domain.StorageCredentials
}

func (s *Service) StorageCredentials(ctx context.Context, version, binding, pool string) (StorageGrant, error) {
	if s.Credentials == nil ||
		s.Config == nil ||
		s.Config.ObjectStorage == nil ||
		s.Config.ObjectStorage.CursorKey == "" {
		return StorageGrant{}, domain.Problem(503, "OBJECT_STORAGE_NOT_CONFIGURED", "Object storage is not configured")
	}
	bucket, err := s.Store.RuntimeBucket(ctx, version, binding, pool)
	if err != nil {
		return StorageGrant{}, err
	}
	for _, store := range s.Config.ObjectStorage.Stores {
		if store.ID != bucket.Store {
			continue
		}
		scope := domain.StorageCredentialScope{
			Organization:          bucket.Organization,
			BucketID:              bucket.ID,
			StoreID:               store.ID,
			Bucket:                store.Bucket,
			Prefix:                store.RootPrefix + "/org/" + bucket.Organization + "/buckets/" + bucket.ID + "/",
			Access:                domain.StorageAllAccess,
			AuthorizationRevision: bucket.Revision,
		}
		credentials, err := s.Credentials.Credentials(ctx, scope, 2*time.Minute)
		if err != nil {
			return StorageGrant{}, domain.Problem(503, "STORAGE_CREDENTIALS_UNAVAILABLE", "Storage credentials are unavailable")
		}
		return StorageGrant{
			BucketID:                   bucket.ID,
			StoreID:                    store.ID,
			Endpoint:                   store.Endpoint,
			Region:                     store.Region,
			Bucket:                     store.Bucket,
			RequestTimeoutMilliseconds: s.Config.ObjectStorage.RequestTimeoutSeconds * 1000,
			CursorKey:                  s.Config.ObjectStorage.CursorKey,
			Prefix:                     scope.Prefix,
			AddressingStyle:            store.AddressingStyle,
			AuthorizationRevision:      bucket.Revision,
			LeaseUntil:                 time.Now().UTC().Add(5 * time.Minute),
			Credentials:                credentials,
		}, nil
	}
	return StorageGrant{}, domain.Problem(503, "STORAGE_UNAVAILABLE", "Bucket storage is unavailable")
}
