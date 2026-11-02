package domain

import (
	"time"
)

// Edge configuration is intentionally small: routes and the immutable release
// descriptors they reference. Build logs, operations and IAM state never enter
// this stream.
type WorkerConfigRoute struct {
	Service string `json:"service_id,omitempty"`
	Host    string `json:"host"`
	Path    string `json:"path,omitempty"`
	Enabled bool   `json:"enabled"`
	Release string `json:"release"`
	Pool    string `json:"pool"`
}

type WorkerConfigProjection struct {
	Routes   []WorkerConfigRoute      `json:"routes"`
	Releases map[string]WorkerRelease `json:"releases"`
}

type WorkerConfigOp struct {
	Type    string             `json:"type"`
	Pool    string             `json:"pool"`
	Host    string             `json:"host,omitempty"`
	Route   *WorkerConfigRoute `json:"route,omitempty"`
	Release *WorkerRelease     `json:"release,omitempty"`
}

type WorkerConfigRevision struct {
	Revision int64
	Digest   string
	Payload  []byte
	Ops      []byte
	Created  time.Time
}
