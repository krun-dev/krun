package control

import (
	"context"
	"encoding/json"
)

func loadWorkerConfigProjection(ctx context.Context, store *Store, ids []string, config *WorkersConfig) (workerConfigProjection, error) {
	state, err := store.WorkerConfigState(ctx, ids)
	if err != nil {
		return workerConfigProjection{}, err
	}
	return workerConfigFromState(state, config), nil
}
func ensureWorkerConfigCatalogue(ctx context.Context, store *Store, config *WorkersConfig) error {
	raw, err := json.Marshal(struct {
		Pools    []WorkerRuntimePool
		Domains  []WorkerDomain
		Profiles []WorkerNetworkProfile
	}{config.RuntimePools, config.Domains, config.NetworkProfiles})
	if err != nil {
		return err
	}
	return store.EnsureWorkerConfigCatalogue(ctx, bytesDigest(raw))
}
