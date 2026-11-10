package control

import (
	"context"

	"github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/storage"
)

type Store = storage.Store

type StatePartition = storage.StatePartition

const (
	PartitionIAM     = storage.PartitionIAM
	PartitionWorkers = storage.PartitionWorkers
	PartitionAll     = storage.PartitionAll
)

// OpenStore creates the storage implementation without exposing its driver.
func OpenStore(ctx context.Context, c config.Config) (*Store, error) {
	var keys storage.RepositoryTokenKeys
	if c.Workers != nil {
		keys = storage.RepositoryTokenKeys{
			Active: c.Workers.SecretsActiveKey,
			Lookup: c.Workers.WorkerSecretKey,
		}
	}
	return storage.Open(ctx, c.DB, keys)
}

type workerDirtyWorker = storage.DirtyWorker

type stateReadScope = storage.ReadScope

type stateReadScopeKey = storage.ReadScopeKey

type loginReadScope = storage.LoginScope

type loginReadScopeKey = storage.LoginScopeKey

var withStateScope = storage.WithReadScope

var previewCount = storage.PreviewCount

var itoa = storage.Itoa

const workerArtifactRetain = storage.WorkerArtifactRetain

const auditRetention = storage.AuditRetention

const workerActiveBuildLimit = storage.WorkerActiveBuildLimit

const workerBuildRetainPerWorker = storage.WorkerBuildRetainPerWorker
