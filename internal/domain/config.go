package domain

import (
	"time"
)

// Edge configuration is intentionally small: routes and the immutable version
// descriptors they reference. Build logs, operations and IAM state never enter
// this stream.
type RetainedWorkerVersion struct {
	Version string    `json:"version_id"`
	Expires time.Time `json:"expires_at"`
}

type EdgeConfigRoute struct {
	WorkerGradualRelease
	ScriptName  string                  `json:"script_name,omitempty"`
	Environment string                  `json:"environment,omitempty"`
	Public      bool                    `json:"public,omitempty"`
	Retained    []RetainedWorkerVersion `json:"retained_versions,omitempty"`
	Expires     *time.Time              `json:"expires_at,omitempty"`
	Worker      string                  `json:"worker_id,omitempty"`
	Host        string                  `json:"host"`
	Path        string                  `json:"path,omitempty"`
	Enabled     bool                    `json:"enabled"`
	Version     string                  `json:"version"`
	Pool        string                  `json:"pool"`
}

func (r EdgeConfigRoute) VersionIDs() []string {
	ids := make([]string, 0, 2+len(r.Retained))
	if r.Version != "" {
		ids = append(ids, r.Version)
	}
	if r.Candidate != "" {
		ids = append(ids, r.Candidate)
	}
	for _, version := range r.Retained {
		ids = append(ids, version.Version)
	}
	return ids
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

type EdgeConfigCursor struct {
	Epoch    string `json:"config_epoch"`
	Revision int64  `json:"config_revision"`
}
