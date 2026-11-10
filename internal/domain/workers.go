package domain

import (
	"time"
)

type WorkerAssetsConfig struct {
	BasePath  string `json:"base_path,omitempty"`
	Headers   string `json:"_headers,omitempty"`
	Redirects string `json:"_redirects,omitempty"`

	Directory        string   `json:"directory"`
	Binding          string   `json:"binding,omitempty"`
	RunWorkerFirst   []string `json:"run_worker_first,omitempty"`
	NotFoundHandling string   `json:"not_found_handling,omitempty"`
	HTMLHandling     string   `json:"html_handling,omitempty"`
}

type WorkerActor struct {
	User        string
	Key         string
	AuthVersion int
}

type WorkerBuild struct {
	Repository         *WorkerBuildRepository `json:"repository_source,omitempty"`
	Assets             *WorkerAssetsConfig    `json:"assets,omitempty"`
	ID                 string                 `json:"id"`
	Organization       string                 `json:"organization_id"`
	Worker             string                 `json:"worker_id"`
	Environment        string                 `json:"environment"`
	State              string                 `json:"state"`
	Entrypoint         string                 `json:"entrypoint"`
	SourceDigest       string                 `json:"source_sha256"`
	Profile            string                 `json:"profile"`
	CompatibilityDate  string                 `json:"compatibility_date"`
	CompatibilityFlags []string               `json:"compatibility_flags,omitempty"`
	Job                string                 `json:"job"`
	Artifact           string                 `json:"artifact_id,omitempty"`
	Error              string                 `json:"error,omitempty"`
	Created            time.Time              `json:"created_at"`
	Finished           *time.Time             `json:"finished_at,omitempty"`
	// Claim metadata participates in storage CAS only. It is never returned by
	// the API and cannot be changed through the generic snapshot writer.
	ClaimedBy string     `json:"-"`
	ClaimedAt *time.Time `json:"-"`
}

// Private job inputs and credentials are not returned by Build APIs.
type WorkerBuildPrivate struct {
	Actor     WorkerActor
	SourceKey string
	Files     map[string]string
	LogKey    string
}

type WorkerArtifact struct {
	ID                 string    `json:"id"`
	Organization       string    `json:"organization_id"`
	State              string    `json:"state"`
	Profile            string    `json:"profile"`
	ArchiveDigest      string    `json:"archive_sha256"`
	ManifestDigest     string    `json:"manifest_sha256"`
	ModuleDigest       string    `json:"module_sha256"`
	CompatibilityDate  string    `json:"compatibility_date"`
	CompatibilityFlags []string  `json:"compatibility_flags,omitempty"`
	Size               int       `json:"archive_size_bytes"`
	ObjectKey          string    `json:"object_key"`
	StorageLocation    string    `json:"storage_location_id"`
	Bucket             string    `json:"bucket"`
	Created            time.Time `json:"created_at"`
	// UseRevision arbitrates build reservations against object-store GC. It is
	// storage coordination metadata and is never exposed by the API.
	UseRevision int64 `json:"-"`
}

type WorkerEnvironment struct {
	HyperdriveBindings map[string]string          `json:"hyperdrive_bindings,omitempty"`
	R2Bindings         map[string]WorkerR2Binding `json:"r2_buckets,omitempty"`
	VersionMetadata    string                     `json:"version_metadata,omitempty"`
	AssetsBinding      string                     `json:"assets_binding,omitempty"`
	Name               string                     `json:"name"`
	Enabled            bool                       `json:"enabled"`
	// Domains mirrors the worker-level selection, in catalog order. Routing
	// decisions live on the worker; this copy keeps each environment row
	// self-describing for projections and API responses.
	Domains []string `json:"domains"`
	// Hosts holds one route host per selected domain: {slug}-{env}.{domain}.
	// Disabled environments keep their hosts so the runtime can answer 410.
	Hosts []string `json:"hosts"`
	// DisplayHosts maps domain id to the browser-facing form of its host.
	// Presentation only: routing uses Hosts.
	DisplayHosts   map[string]string `json:"display_hosts,omitempty"`
	ConfigRevision int               `json:"configuration_revision"`
	Variables      WorkerVariables   `json:"variables"`
	// SecretNames is safe metadata for the console. SecretVersions is the
	// internal immutable binding map and is deliberately absent from JSON.
	SecretNames    []string          `json:"secret_names"`
	SecretVersions map[string]string `json:"-"`
	Revision       int               `json:"deployment_revision"`
	Version        string            `json:"version_id,omitempty"`
	Confirmed      string            `json:"confirmed_version_id,omitempty"`
}

type Worker struct {
	PreviewBase        *WorkerEnvironment      `json:"previews_base,omitempty"`
	Repository         *WorkerRepositorySource `json:"repository_source,omitempty"`
	RepositoryRevision int                     `json:"repository_revision"`
	// DomainsRevision is the optimistic-concurrency token for replacing the
	// worker's routing domains. PreviewRevision is internal coordination for
	// changes to the preview-environment set; it prevents concurrent creates
	// from both passing the configured preview limit.
	DomainsRevision int    `json:"domains_revision"`
	PreviewRevision int    `json:"preview_revision"`
	Worker          string `json:"worker_id"`
	Organization    string `json:"organization_id"`
	// NetworkProfile is the stable product intent selected for the worker.
	NetworkProfile string `json:"network_profile"`
	// Domains is the worker-level domain selection. Every environment of the
	// worker is served on every selected domain, so adding a domain adds one
	// host per environment without touching per-environment configuration.
	Domains      []string                     `json:"domains"`
	Environments map[string]WorkerEnvironment `json:"environments"`
}

// WorkerPlacement is the infrastructure assignment for one worker. It is a
// separate entity because a worker may temporarily have more than one
// placement during a platform-managed migration. Organization APIs never
// expose pool IDs.
type WorkerPlacement struct {
	Worker         string    `json:"worker_id"`
	Organization   string    `json:"organization_id"`
	NetworkProfile string    `json:"network_profile"`
	Pool           string    `json:"pool"`
	Role           string    `json:"role"`
	State          string    `json:"state"`
	Revision       int       `json:"revision"`
	Created        time.Time `json:"created_at"`
	Updated        time.Time `json:"updated_at"`
}

type WorkerVersion struct {
	SecretNames        []string                   `json:"secret_names,omitempty"`
	Configuration      string                     `json:"configuration_id,omitempty"`
	CreatedBy          string                     `json:"created_by"`
	CreatedByName      string                     `json:"created_by_name"`
	HyperdriveBindings map[string]string          `json:"hyperdrive_bindings,omitempty"`
	R2Bindings         map[string]WorkerR2Binding `json:"r2_buckets,omitempty"`
	VersionMetadata    string                     `json:"version_metadata,omitempty"`
	AssetsBinding      string                     `json:"assets_binding,omitempty"`
	Tag                string                     `json:"tag,omitempty"`
	Message            string                     `json:"message,omitempty"`
	ID                 string                     `json:"id"`
	Worker             string                     `json:"worker_id"`
	Organization       string                     `json:"organization_id"`
	Environment        string                     `json:"environment"`
	Artifact           string                     `json:"artifact_id"`
	Profile            string                     `json:"profile"`
	ArchiveDigest      string                     `json:"archive_sha256"`
	ModuleDigest       string                     `json:"module_sha256"`
	CompatibilityDate  string                     `json:"compatibility_date"`
	CompatibilityFlags []string                   `json:"compatibility_flags,omitempty"`
	ConfigRevision     int                        `json:"configuration_revision"`
	Variables          WorkerVariables            `json:"variables"`
	// SecretDigest is safe to distribute with the edge descriptor. The
	// version IDs and values stay in PostgreSQL and are fetched only through
	// the authenticated runtime endpoint.
	SecretDigest   string            `json:"secret_digest,omitempty"`
	SecretVersions map[string]string `json:"-"`
	Created        time.Time         `json:"created_at"`
}

// WorkerSecretVersion is immutable metadata. Ciphertext is intentionally not
// present: generic state loads must never materialize secret values.
type WorkerSecretVersion struct {
	ID           string    `json:"id"`
	Organization string    `json:"organization_id"`
	Worker       string    `json:"worker_id"`
	Environment  string    `json:"environment"`
	Name         string    `json:"name"`
	ValueDigest  string    `json:"value_sha256"`
	KeyVersion   string    `json:"key_version"`
	CreatedBy    string    `json:"created_by"`
	Created      time.Time `json:"created_at"`
}

type WorkerStep struct {
	Name   string `json:"name"`
	State  string `json:"state"`
	Detail string `json:"detail,omitempty"`
}

type WorkerDeployment struct {
	ID           string     `json:"id"`
	Organization string     `json:"organization_id"`
	Worker       string     `json:"worker_id"`
	Environment  string     `json:"environment"`
	Version      string     `json:"version_id"`
	Operation    string     `json:"operation_id"`
	Created      time.Time  `json:"created_at"`
	Expires      time.Time  `json:"expires_at"`
	Committed    *time.Time `json:"committed_at,omitempty"`
}

type WorkerOperation struct {
	ID                 string       `json:"id"`
	Kind               string       `json:"kind"`
	Worker             string       `json:"worker_id"`
	Organization       string       `json:"organization_id"`
	Environment        string       `json:"environment"`
	State              string       `json:"state"`
	Build              string       `json:"build_id,omitempty"`
	Version            string       `json:"version_id"`
	ExpectedRevision   int          `json:"expected_revision"`
	ConfigRevision     int          `json:"configuration_revision"`
	EdgeRevision       int          `json:"edge_revision,omitempty"`
	DeploymentRevision int          `json:"deployment_revision,omitempty"`
	Error              string       `json:"error,omitempty"`
	Steps              []WorkerStep `json:"steps"`
	Created            time.Time    `json:"created_at"`
	Finished           *time.Time   `json:"finished_at,omitempty"`
}

type WorkerOperationPrivate struct{ Actor WorkerActor }

type WorkerHistory struct {
	Revision  int       `json:"revision"`
	Version   string    `json:"version_id"`
	Operation string    `json:"operation_id"`
	Kind      string    `json:"kind"`
	Time      time.Time `json:"time"`
}

type WorkerIdempotency struct {
	Digest string
	ID     string
}

type WorkerAck struct {
	Pod      string    `json:"pod"`
	UID      string    `json:"pod_uid"`
	Pool     string    `json:"pool"`
	Revision int       `json:"revision"`
	Time     time.Time `json:"time"`
}

type WorkerState struct {
	Configurations   map[string]WorkerConfiguration
	Buckets          map[string]WorkerBucket
	Revision         int
	Workers          map[string]Worker
	Placements       map[string]WorkerPlacement
	Builds           map[string]WorkerBuild
	BuildPrivate     map[string]WorkerBuildPrivate
	Artifacts        map[string]WorkerArtifact
	Deployments      map[string]WorkerDeployment
	Versions         map[string]WorkerVersion
	Operations       map[string]WorkerOperation
	OperationPrivate map[string]WorkerOperationPrivate
	Histories        map[string][]WorkerHistory
	Idempotency      map[string]WorkerIdempotency
	SecretVersions   map[string]WorkerSecretVersion
}

// WorkerScript is a protocol identity, not a mutable resource display name.
