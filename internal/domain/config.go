package domain

import (
	"time"
)

// Edge configuration is intentionally small: routes and the immutable version
// descriptors they reference. Build logs, operations and IAM state never enter
// this stream.
type EdgeConfigRoute struct {
	Expires *time.Time `json:"expires_at,omitempty"`
	Worker  string     `json:"worker_id,omitempty"`
	Host    string     `json:"host"`
	Path    string     `json:"path,omitempty"`
	Enabled bool       `json:"enabled"`
	Version string     `json:"version"`
	Pool    string     `json:"pool"`
}

type EdgeConfigProjection struct {
	Routes   []EdgeConfigRoute        `json:"routes"`
	Versions map[string]WorkerVersion `json:"versions"`
}

type EdgeConfigOp struct {
	Type    string           `json:"type"`
	Pool    string           `json:"pool"`
	Host    string           `json:"host,omitempty"`
	Route   *EdgeConfigRoute `json:"route,omitempty"`
	Version *WorkerVersion   `json:"version,omitempty"`
}

type EdgeConfigRevision struct {
	Revision int64
	Digest   string
	Ops      []byte
	Created  time.Time
}
