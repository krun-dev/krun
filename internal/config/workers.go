package config

// Workers is opt-in: existing authentication deployments keep their current behavior.
type WorkersConfig struct {
	ReconcileConcurrency int                        `json:"reconcile_concurrency"`
	RepositoryProviders  []WorkerRepositoryProvider `json:"repository_providers"`
	Namespace            string                     `json:"namespace"`
	BuilderImage         string                     `json:"builder_image"`
	BuilderPullPolicy    string                     `json:"builder_pull_policy"`
	// BuilderRuntimeClass selects an optional Kubernetes RuntimeClass for
	// untrusted project builds (for example gVisor or Kata Containers).
	BuilderRuntimeClass string `json:"builder_runtime_class"`
	// BuilderTimeoutSeconds bounds both Kubernetes Jobs and trusted local-dev
	// builds. Project dependency installation needs a larger budget than the
	// direct single-file compiler.
	BuilderTimeoutSeconds int `json:"builder_timeout_seconds"`
	// BuilderMode selects the build executor: "kubernetes" creates a Job;
	// "local" runs workers/builder/build-worker.mjs on the control-plane host.
	BuilderMode   string `json:"builder_mode"`
	BuilderScript string `json:"builder_script"`
	// NPMRegistry is the single trusted public/package-proxy origin accepted by
	// package-lock files. Credentials are intentionally not supported here.
	NPMRegistry string `json:"npm_registry"`
	// RuntimePools is infrastructure-only. Organization APIs expose only the
	// deployment-defined network profiles; access modes, pool ids and services
	// never leave the system scope.
	RuntimePools []WorkerRuntimePool `json:"runtime_pools"`
	// NetworkProfiles are the deployment-defined product choices shown to
	// organization users. Profiles describe intent; pool ids, Gateway names and
	// enforcement details remain infrastructure-only.
	NetworkProfiles []WorkerNetworkProfile `json:"network_profiles"`
	// Kubernetes connection settings are regular application configuration.
	// They intentionally live here instead of being read from hidden process
	// environment variables so every runtime input passes through LoadConfig.
	KubernetesAPI       string `json:"kubernetes_api"`
	KubernetesTokenFile string `json:"kubernetes_token_file"`
	KubernetesCAFile    string `json:"kubernetes_ca_file"`
	// Gateways and Domains are the routing catalog. Neither carries a network
	// classification: a domain is just a suffix plus which Gateway instance
	// serves it. Label is display text and never participates in routing.
	Gateways []WorkerGateway `json:"gateways"`
	Domains  []WorkerDomain  `json:"domains"`
	// SecretsKeys is a versioned ring of base64-encoded 32-byte AES keys used
	// only by the control plane. Keep old versions while retained Versions
	// reference them; new writes use SecretsActiveKey.
	SecretsKeys       map[string]string `json:"secrets_keys"`
	SecretsActiveKey  string            `json:"secrets_active_key"`
	S3Endpoint        string            `json:"s3_endpoint"`
	S3BuilderEndpoint string            `json:"s3_builder_endpoint"`
	S3RuntimeEndpoint string            `json:"s3_runtime_endpoint"`
	S3Region          string            `json:"s3_region"`
	S3AddressingStyle string            `json:"s3_addressing_style"`
	S3Prefix          string            `json:"s3_prefix"`
	S3AccessMode      string            `json:"s3_access_mode"`
	S3BucketMode      string            `json:"s3_bucket_mode"`
	S3Bucket          string            `json:"s3_bucket"`
	S3LocationID      string            `json:"s3_location_id"`
	S3AccessKey       string            `json:"s3_access_key"`
	S3SecretKey       string            `json:"s3_secret_key"`
	PreviewLimit      int               `json:"preview_limit"`
	StaticService     string            `json:"static_service"`
	StaticServicePort int               `json:"static_service_port"`
	// ConfigBaseURL is the runtime-readable origin of config/latest.json and
	// config/pools/<pool>/shards/*. Signed access works with any S3 provider.
	ConfigBaseURL string `json:"config_base_url"`
	// CloneDirectory is where repository checkouts are written during a build.
	// Required when the control plane runs with a read-only root filesystem.
	CloneDirectory string `json:"clone_directory"`
}

type WorkerRuntimePool struct {
	ID             string            `json:"id"`
	Access         []string          `json:"access"`
	Labels         map[string]string `json:"labels"`
	PrivateEgress  string            `json:"private_egress"`
	PublicEgress   string            `json:"public_egress"`
	RuntimeService string            `json:"runtime_service"`
	// ValidatorService selects a dedicated workerd Deployment that loads version
	// candidates before commit. It must not be the serving RuntimeService.
	ValidatorService        string `json:"validator_service"`
	ValidatorManagementBase string `json:"validator_management_base"`
	RuntimeToken            string `json:"runtime_token"`
	ManagementToken         string `json:"management_token"`
	Default                 bool   `json:"default"`
}

type WorkerNetworkRequirements struct {
	Ingress       []string `json:"ingress"`
	PrivateEgress string   `json:"private_egress"`
	PublicEgress  string   `json:"public_egress"`
}

// WorkerNetworkProfile is a configurable product-facing bundle. It deliberately
// contains selectors and requirements instead of a pool id, so changing the
// infrastructure layout does not leak into organization APIs or saved intent.
type WorkerNetworkProfile struct {
	ID             string                    `json:"id"`
	Name           string                    `json:"name"`
	Description    string                    `json:"description"`
	DomainSelector map[string]string         `json:"domain_selector"`
	PoolSelector   map[string]string         `json:"pool_selector"`
	Requirements   WorkerNetworkRequirements `json:"requirements"`
	Approval       string                    `json:"approval"`
	Visibility     string                    `json:"visibility"`
	Default        bool                      `json:"default"`
}

// WorkerGateway is one Gateway instance the control plane writes routes to.
// Separate instances are the isolation boundary: a request entering instance A
// can only match hostnames present in A's route table.
type WorkerGateway struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Section   string `json:"section"`
	// Base overrides the address console invoke uses for this instance, e.g. an
	// SSH tunnel. Empty means <name>.<namespace>.svc:8080.
	Base string `json:"base"`
}

// WorkerDomain is one browser-facing suffix users can attach to a service.
// Access, Labels and Gateway are infrastructure metadata used to resolve a
// Network Profile. Only the filtered domain id/name/display label is returned
// to organization users.
type WorkerDomain struct {
	ID      string `json:"id"`
	Domain  string `json:"domain"`
	Label   string `json:"label"`
	Gateway string `json:"gateway"`
	// Access is a stable infrastructure primitive used by profile requirements.
	// It is deliberately not returned to organization APIs.
	Access string `json:"access"`
	// Labels are deployment metadata used by Network Profile selectors. They
	// are not returned to organization APIs.
	Labels map[string]string `json:"labels"`
	// Default selects this domain for newly created environments.
	Default bool `json:"default"`
	// StaticDomain overrides the static-asset host served through this domain's
	// Gateway. Empty derives "static." + Domain.
	StaticDomain string `json:"static_domain"`
}
