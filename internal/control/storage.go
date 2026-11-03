package control

import (
	"context"

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
func OpenStore(ctx context.Context, dsn string) (*Store, error) { return storage.Open(ctx, dsn) }

type workerDirtyService = storage.DirtyService

type workerSourceUpload = storage.SourceUpload

type stateReadScope = storage.ReadScope

type stateReadScopeKey = storage.ReadScopeKey

type loginReadScope = storage.LoginScope

type loginReadScopeKey = storage.LoginScopeKey

type workerConfigPriorityKey = storage.WorkerConfigPriorityKey

type workerRuntimeFeedPoolKey = storage.WorkerRuntimeFeedPoolKey

type workerQueueReplicaKey = storage.WorkerQueueReplicaKey

var withStateScope = storage.WithReadScope

var errArtifactBeingCollected = storage.ErrArtifactBeingCollected

var errBuildClaimLost = storage.ErrBuildClaimLost

var previewCount = storage.PreviewCount

var itoa = storage.Itoa

const workerArtifactRetain = storage.WorkerArtifactRetain

const workerUploadRetain = storage.WorkerUploadRetain

const auditRetention = storage.AuditRetention

const workerConfigOpsRetain = storage.WorkerConfigOpsRetain

const workerActiveBuildLimit = storage.WorkerActiveBuildLimit

const workerBuildRetainPerService = storage.WorkerBuildRetainPerService
