package domain

import (
	"time"
)

// Edge configuration is intentionally small: routes and the immutable version
// descriptors they reference. Build logs, operations and IAM state never enter
// this stream.
type WorkerConfigRoute struct {
	Expires *time.Time `json:"expires_at,omitempty"`
	Worker  string     `json:"worker_id,omitempty"`
	Host    string     `json:"host"`
	Path    string     `json:"path,omitempty"`
	Enabled bool       `json:"enabled"`
	Version string     `json:"version"`
	Pool    string     `json:"pool"`
}

type WorkerConfigProjection struct {
	Routes   []WorkerConfigRoute      `json:"routes"`
	Versions map[string]WorkerVersion `json:"versions"`
}

type WorkerConfigOp struct {
	Type    string             `json:"type"`
	Pool    string             `json:"pool"`
	Host    string             `json:"host,omitempty"`
	Route   *WorkerConfigRoute `json:"route,omitempty"`
	Version *WorkerVersion     `json:"version,omitempty"`
}

type WorkerConfigRevision struct {
	Revision int64
	Digest   string
	Ops      []byte
	Created  time.Time
}
