package domain

import "time"

// WorkerConfiguration is an immutable, environment-scoped configuration. It
// has no code or routing state; publishing combines it with an explicit code
// version and creates an ordinary WorkerVersion and deployment.
type WorkerConfiguration struct {
	ID              string                     `json:"id"`
	Organization    string                     `json:"organization_id"`
	Worker          string                     `json:"worker_id"`
	Environment     string                     `json:"environment"`
	Name            string                     `json:"name"`
	Message         string                     `json:"message,omitempty"`
	Variables       WorkerVariables            `json:"variables"`
	R2Bindings      map[string]WorkerR2Binding `json:"r2_buckets,omitempty"`
	VersionMetadata string                     `json:"version_metadata,omitempty"`
	AssetsBinding   string                     `json:"assets_binding,omitempty"`
	SecretNames     []string                   `json:"secret_names"`
	SecretVersions  map[string]string          `json:"-"`
	Created         time.Time                  `json:"created_at"`
}
