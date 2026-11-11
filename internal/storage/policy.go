package storage

import (
	"github.com/krun-dev/krun/internal/securetoken"
	"time"
)

const AuditRetention = 7 * 24 * time.Hour
const WorkerUploadRetain = 24 * time.Hour
const WorkerConfigOpsRetain = 512

func now() time.Time                { return time.Now().UTC() }
func digest(value string) string    { return bytesDigest([]byte(value)) }
func bytesDigest(raw []byte) string { return securetoken.DigestBytes(raw) }

const workerBaseline = "production"

// Retained row limits. Every table that grows per request, per build or per
// version has a bound here and a matching DELETE in retention.go.
const (
	WorkerBuildRetainPerWorker = 100
	workerBuildLogRetain       = 7 * 24 * time.Hour
	workerOperationRetain      = 30 * 24 * time.Hour
	WorkerArtifactRetain       = 30 * 24 * time.Hour
	workerIdempotencyRetain    = 7 * 24 * time.Hour
	WorkerHistoryRetainPerEnv  = 50
)
