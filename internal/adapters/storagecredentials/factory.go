package storagecredentials

import (
	"context"
	"strings"
	"time"

	"github.com/krun-dev/krun/internal/adapters/storagecredentials/provider"
	"github.com/krun-dev/krun/internal/adapters/storagecredentials/staticcred"
	"github.com/krun-dev/krun/internal/adapters/storagecredentials/tos"
	"github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/domain"
)

type storeManager struct {
	manager    *Manager
	bucket     string
	rootPrefix string
}

type Registry struct{ stores map[string]storeManager }

func NewRegistry(c *config.WorkerObjectStorageConfig) (*Registry, error) {
	registry := &Registry{stores: make(map[string]storeManager)}
	if c == nil {
		return registry, nil
	}
	// Validate a private copy so construction never mutates live application config.
	copy := *c
	copy.Stores = append([]config.WorkerObjectStore(nil), c.Stores...)
	if err := copy.Validate(); err != nil {
		return nil, &provider.Error{Kind: provider.InvalidConfiguration}
	}
	for _, store := range copy.Stores {
		source, err := credentialProvider(store.Credentials, store.Region)
		if err != nil {
			return nil, err
		}
		manager, err := New(source, Options{
			TTL:              time.Duration(copy.CredentialTTLSeconds) * time.Second,
			RefreshBefore:    time.Duration(copy.RefreshBeforeSeconds) * time.Second,
			Timeout:          time.Duration(copy.CredentialTimeoutSeconds) * time.Second,
			CacheEntries:     copy.CredentialCacheEntries,
			MaxConcurrent:    copy.CredentialConcurrency,
			RequireTemporary: store.Credentials.Type != "static",
		})
		if err != nil {
			return nil, err
		}
		registry.stores[store.ID] = storeManager{
			manager:    manager,
			bucket:     store.Bucket,
			rootPrefix: store.RootPrefix + "/",
		}
	}
	return registry, nil
}

func (r *Registry) Credentials(ctx context.Context, scope domain.StorageCredentialScope, minimum time.Duration) (domain.StorageCredentials, error) {
	store, ok := r.stores[scope.StoreID]
	if !ok || scope.Bucket != store.bucket || !strings.HasPrefix(scope.Prefix, store.rootPrefix) {
		return domain.StorageCredentials{}, &provider.Error{Kind: provider.InvalidScope}
	}
	return store.manager.Credentials(ctx, scope, minimum)
}

// NewArtifactManager reuses the STS provider and cache implementation while
// keeping artifact read scopes separate from business storage.
func NewArtifactManager(c *config.WorkersConfig) (*Manager, error) {
	credentials := config.WorkerStorageCredentialConfig{
		Type:            "static",
		AccessKeyID:     c.S3AccessKey,
		SecretAccessKey: c.S3SecretKey,
	}
	if c.S3RuntimeCredentials != nil {
		credentials = *c.S3RuntimeCredentials
	}
	source, err := credentialProvider(credentials, c.S3Region)
	if err != nil {
		return nil, err
	}
	return New(source, Options{
		TTL:              time.Hour,
		RefreshBefore:    5 * time.Minute,
		Timeout:          20 * time.Second,
		CacheEntries:     1024,
		MaxConcurrent:    8,
		RequireTemporary: credentials.Type != "static",
	})
}

func credentialProvider(credentials config.WorkerStorageCredentialConfig, region string) (provider.Provider, error) {
	switch credentials.Type {
	case "static":
		return staticcred.New(credentials.AccessKeyID, credentials.SecretAccessKey)
	case "tos_assume_role":
		return tos.New(tos.Options{
			AccessKeyID:     credentials.AccessKeyID,
			SecretAccessKey: credentials.SecretAccessKey,
			RoleTrn:         credentials.RoleTrn,
			Region:          region,
			Endpoint:        credentials.STSEndpoint,
		})
	default:
		return nil, &provider.Error{Kind: provider.InvalidConfiguration}
	}
}
