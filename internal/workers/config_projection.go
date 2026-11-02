package workers

import (
	"context"
	"encoding/json"

	workerartifact "github.com/krun-dev/krun/internal/artifact"
	appconfig "github.com/krun-dev/krun/internal/config"
	domain "github.com/krun-dev/krun/internal/domain"
	storage "github.com/krun-dev/krun/internal/storage"
)

func LoadWorkerConfigProjection(ctx context.Context, store *storage.Store, ids []string, config *appconfig.WorkersConfig) (domain.WorkerConfigProjection, error) {
	state, err := store.WorkerConfigState(ctx, ids)
	if err != nil {
		return domain.WorkerConfigProjection{}, err
	}
	return WorkerConfigFromState(state, config), nil
}

func EnsureWorkerConfigCatalogue(ctx context.Context, store *storage.Store, config *appconfig.WorkersConfig) error {
	raw, err := json.Marshal(struct {
		Pools    []appconfig.WorkerRuntimePool
		Domains  []appconfig.WorkerDomain
		Profiles []appconfig.WorkerNetworkProfile
	}{config.RuntimePools, config.Domains, config.NetworkProfiles})
	if err != nil {
		return err
	}
	return store.EnsureWorkerConfigCatalogue(ctx, workerartifact.BytesDigest(raw))
}
