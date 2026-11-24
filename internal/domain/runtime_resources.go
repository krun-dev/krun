package domain

import (
	"encoding/json"
	"maps"
)

// ResourceRef is scoped by the authenticated pool. Generation is independent
// per resource; incarnation prevents a deleted/recreated key from reusing a view.
type ResourceRef struct {
	Kind        string `json:"kind"`
	ID          string `json:"id"`
	Incarnation string `json:"incarnation,omitempty"`
	Generation  int64  `json:"generation,omitempty"`
}

func (r ResourceRef) Key() string { return r.Kind + ":" + r.ID }

type RuntimeResource struct {
	ResourceRef
	Deleted      bool            `json:"deleted,omitempty"`
	Payload      json.RawMessage `json:"payload,omitempty"`
	Dependencies []ResourceRef   `json:"dependencies,omitempty"`
}

// RuntimeRouteHostMaxBytes is the DNS hostname limit without a trailing dot.
const RuntimeRouteHostMaxBytes = 253

type RuntimeRouteBinding struct {
	EdgeConfigRoute
	Fixed bool `json:"fixed"`
}

type RuntimeDeployment struct {
	WorkerGradualRelease
	Worker      string                  `json:"worker_id"`
	Environment string                  `json:"environment"`
	Version     string                  `json:"version"`
	Enabled     bool                    `json:"enabled"`
	Retained    []RetainedWorkerVersion `json:"retained_versions,omitempty"`
}

type RuntimeScope struct {
	Allowed bool `json:"allowed"`
}

type RuntimePropagation struct {
	Epoch               string `json:"config_epoch"`
	Generation          int64  `json:"target_generation"`
	Incarnation         string `json:"incarnation"`
	State               string `json:"state"`
	MembershipConfirmed bool   `json:"membership_confirmed"`
	Total               int    `json:"total"`
	Applied             int    `json:"applied"`
	Unsubscribed        int    `json:"unsubscribed"`
	Lagging             int    `json:"lagging"`
	Unknown             int    `json:"unknown"`
	Rejected            int    `json:"rejected"`
}

// RuntimeVersionDescriptor strips control-plane-only grants without changing
// the immutable bindings referenced by a version.
func RuntimeVersionDescriptor(version WorkerVersion) WorkerVersion {
	version.VersionURL = false
	version.ServiceBindings = maps.Clone(version.ServiceBindings)
	for name, binding := range version.ServiceBindings {
		binding.GrantedBy = nil
		version.ServiceBindings[name] = binding
	}
	return version
}
