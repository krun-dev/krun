package control

import (
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"
)

// Workers is opt-in: existing authentication deployments keep their current behavior.
type WorkersConfig struct {
	RepositoryProviders []WorkerRepositoryProvider `json:"repository_providers"`
	Namespace           string                     `json:"namespace"`
	BuilderImage        string                     `json:"builder_image"`
	BuilderPullPolicy   string                     `json:"builder_pull_policy"`
	RuntimeService      string                     `json:"runtime_service"`
	GatewayName         string                     `json:"gateway_name"`
	GatewayNamespace    string                     `json:"gateway_namespace"`
	Domain              string                     `json:"domain"`
	RuntimeToken        string                     `json:"runtime_token"`
	ManagementToken     string                     `json:"management_token"`
	RuntimeManagementBase string                    `json:"runtime_management_base"`
	S3Endpoint          string                     `json:"s3_endpoint"`
	S3BuilderEndpoint   string                     `json:"s3_builder_endpoint"`
	S3Bucket            string                     `json:"s3_bucket"`
	S3LocationID        string                     `json:"s3_location_id"`
	S3AccessKey         string                     `json:"s3_access_key"`
	S3SecretKey         string                     `json:"s3_secret_key"`
	PreviewLimit        int                        `json:"preview_limit"`
	StaticDomain        string                     `json:"static_domain"`
	StaticInternalDomain string                    `json:"static_internal_domain"`
	StaticService       string                     `json:"static_service"`
	StaticServicePort   int                        `json:"static_service_port"`
}

type WorkerAssetsConfig struct {
	Directory        string   `json:"directory"`
	RunWorkerFirst   []string `json:"run_worker_first,omitempty"`
	NotFoundHandling string   `json:"not_found_handling,omitempty"`
	HTMLHandling     string   `json:"html_handling,omitempty"`
}

func (c *WorkersConfig) validate() error {
	if c == nil {
		return nil
	}
	if err := c.validateRepositoryProviders(); err != nil {
		return err
	}
	if c.BuilderPullPolicy == "" {
		c.BuilderPullPolicy = "IfNotPresent"
	}
	if !contains([]string{"Always", "IfNotPresent", "Never"}, c.BuilderPullPolicy) {
		return fmt.Errorf("invalid Workers builder pull policy")
	}
	if c.S3LocationID == "" {
		c.S3LocationID = "primary"
	}
	if c.RuntimeService == "" {
		c.RuntimeService = "runtime-default"
	}
	if c.GatewayName == "" {
		c.GatewayName = "workers-mvp"
	}
	if c.GatewayNamespace == "" {
		c.GatewayNamespace = "kgateway-system"
	}
	if c.Domain == "" {
		c.Domain = "workers.test"
	}
	dns := regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,180}$`)
	for _, v := range []string{c.Namespace, c.RuntimeService, c.GatewayName, c.GatewayNamespace, c.Domain, c.S3Bucket, c.S3LocationID} {
		if !dns.MatchString(v) {
			return fmt.Errorf("invalid Workers namespace, route or bucket")
		}
	}
	u, e := url.Parse(c.S3Endpoint)
	if e != nil || u.Host == "" || u.Path != "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("invalid Workers S3 endpoint")
	}
	if c.S3BuilderEndpoint != "" {
		u, e := url.Parse(c.S3BuilderEndpoint)
		if e != nil || u.Host == "" || u.Path != "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
			return fmt.Errorf("invalid Workers S3 builder endpoint")
		}
	} else {
		c.S3BuilderEndpoint = c.S3Endpoint
	}
	if c.BuilderImage == "" || len(c.RuntimeToken) < 24 || len(c.ManagementToken) < 24 || c.S3AccessKey == "" || c.S3SecretKey == "" {
		return fmt.Errorf("Workers image and credentials required")
	}
	if c.RuntimeManagementBase != "" {
		u, e := url.Parse(c.RuntimeManagementBase)
		if e != nil || u.Host == "" || u.Path != "" && u.Path != "/" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
			return fmt.Errorf("invalid Workers runtime management base")
		}
		c.RuntimeManagementBase = strings.TrimSuffix(c.RuntimeManagementBase, "/")
	}
	if c.PreviewLimit == 0 {
		c.PreviewLimit = 32
	}
	if c.PreviewLimit < 1 || c.PreviewLimit > 128 {
		return fmt.Errorf("preview_limit must be between 1 and 128")
	}
	if c.StaticDomain == "" {
		c.StaticDomain = "static." + c.Domain
	}
	if c.StaticService == "" {
		c.StaticService = "static-assets"
	}
	if c.StaticServicePort == 0 {
		c.StaticServicePort = 9000
	}
	for _, v := range []string{c.StaticDomain, c.StaticInternalDomain, c.StaticService} {
		if v != "" && !dns.MatchString(v) {
			return fmt.Errorf("invalid Workers static domain or service")
		}
	}
	return nil
}

type WorkerActor struct {
	User        string
	Key         string
	AuthVersion int
}
type WorkerBuild struct {
	Repository   *WorkerBuildRepository `json:"repository_source,omitempty"`
	Assets       *WorkerAssetsConfig    `json:"assets,omitempty"`
	ID           string                 `json:"id"`
	Organization string                 `json:"organization_id"`
	Service      string                 `json:"service_id"`
	Environment  string                 `json:"environment"`
	State        string                 `json:"state"`
	Entrypoint   string                 `json:"entrypoint"`
	SourceDigest string                 `json:"source_sha256"`
	Profile      string                 `json:"profile"`
	Job          string                 `json:"job"`
	Artifact     string                 `json:"artifact_id,omitempty"`
	Error        string                 `json:"error,omitempty"`
	Created      time.Time              `json:"created_at"`
	Finished     *time.Time             `json:"finished_at,omitempty"`
}

// Private job inputs and credentials are not returned by Build APIs.
type WorkerBuildPrivate struct {
	Actor     WorkerActor
	SourceKey string
	Files     map[string]string
	Log       string
}
type WorkerArtifact struct {
	ID              string    `json:"id"`
	Organization    string    `json:"organization_id"`
	State           string    `json:"state"`
	ArchiveDigest   string    `json:"archive_sha256"`
	ManifestDigest  string    `json:"manifest_sha256"`
	ModuleDigest    string    `json:"module_sha256"`
	Size            int       `json:"archive_size_bytes"`
	ObjectKey       string    `json:"object_key"`
	StorageLocation string    `json:"storage_location_id"`
	Bucket          string    `json:"bucket"`
	Created         time.Time `json:"created_at"`
}
type WorkerEnvironment struct {
	Name           string            `json:"name"`
	Enabled        bool              `json:"enabled"`
	Host           string            `json:"host"`
	Visibility     string            `json:"visibility"`
	ConfigRevision int               `json:"configuration_revision"`
	Variables      map[string]string `json:"variables"`
	Revision       int               `json:"release_revision"`
	Release        string            `json:"release_id,omitempty"`
	Confirmed      string            `json:"confirmed_release_id,omitempty"`
}
type WorkerService struct {
	Repository         *WorkerRepositorySource      `json:"repository_source,omitempty"`
	RepositoryRevision int                          `json:"repository_revision"`
	Service            string                       `json:"service_id"`
	Organization       string                       `json:"organization_id"`
	Environments       map[string]WorkerEnvironment `json:"environments"`
}
type WorkerRelease struct {
	ID             string            `json:"id"`
	Service        string            `json:"service_id"`
	Organization   string            `json:"organization_id"`
	Environment    string            `json:"environment"`
	Artifact       string            `json:"artifact_id"`
	ArchiveDigest  string            `json:"archive_sha256"`
	ModuleDigest   string            `json:"module_sha256"`
	ConfigRevision int               `json:"configuration_revision"`
	Variables      map[string]string `json:"variables"`
	Pool           string            `json:"pool"`
	Created        time.Time         `json:"created_at"`
}
type WorkerStep struct {
	Name   string `json:"name"`
	State  string `json:"state"`
	Detail string `json:"detail,omitempty"`
}
type WorkerOperation struct {
	ID               string       `json:"id"`
	Kind             string       `json:"kind"`
	Service          string       `json:"service_id"`
	Organization     string       `json:"organization_id"`
	Environment      string       `json:"environment"`
	State            string       `json:"state"`
	Build            string       `json:"build_id,omitempty"`
	Release          string       `json:"release_id"`
	ExpectedRevision int          `json:"expected_revision"`
	ConfigRevision   int          `json:"configuration_revision"`
	SnapshotRevision int          `json:"snapshot_revision,omitempty"`
	ReleaseRevision  int          `json:"release_revision,omitempty"`
	Error            string       `json:"error,omitempty"`
	Steps            []WorkerStep `json:"steps"`
	Created          time.Time    `json:"created_at"`
	Finished         *time.Time   `json:"finished_at,omitempty"`
}
type WorkerOperationPrivate struct{ Actor WorkerActor }
type WorkerHistory struct {
	Revision   int       `json:"revision"`
	Release    string    `json:"release_id"`
	Operation  string    `json:"operation_id"`
	Kind       string    `json:"kind"`
	Time       time.Time `json:"time"`
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
	Revision         int
	Services         map[string]WorkerService
	Builds           map[string]WorkerBuild
	BuildPrivate     map[string]WorkerBuildPrivate
	Artifacts        map[string]WorkerArtifact
	Releases         map[string]WorkerRelease
	Operations       map[string]WorkerOperation
	OperationPrivate map[string]WorkerOperationPrivate
	Histories        map[string][]WorkerHistory
	Idempotency      map[string]WorkerIdempotency
	Acks             map[string]WorkerAck
}

func newWorkerState() *WorkerState {
	return &WorkerState{Revision: 1, Services: map[string]WorkerService{}, Builds: map[string]WorkerBuild{}, BuildPrivate: map[string]WorkerBuildPrivate{}, Artifacts: map[string]WorkerArtifact{}, Releases: map[string]WorkerRelease{}, Operations: map[string]WorkerOperation{}, OperationPrivate: map[string]WorkerOperationPrivate{}, Histories: map[string][]WorkerHistory{}, Idempotency: map[string]WorkerIdempotency{}, Acks: map[string]WorkerAck{}}
}
// A service has one durable baseline ("production") plus any number of
// short-lived preview environments identified by a slug.
const workerBaseline = "production"

func workerEnvValid(v string) bool { return v == workerBaseline || workerPreviewValid(v) }
func workerPreviewValid(v string) bool {
	return v != workerBaseline && slug.MatchString(v)
}
func workerTerminal(v string) bool {
	return contains([]string{"succeeded", "failed", "available", "superseded"}, v)
}
func workerActor(p Principal) WorkerActor {
	a := WorkerActor{User: p.User.ID, AuthVersion: p.User.AuthVersion}
	if p.Key != nil {
		a.Key = p.Key.ID
	}
	return a
}
func sourceValid(entry string, files map[string]string) error {
	if len(files) == 0 || len(files) > 32 {
		return problem(422, "WORKER_SOURCE_INVALID", "Provide 1–32 source files")
	}
	total := 0
	valid := regexp.MustCompile(`^[a-zA-Z0-9_./-]+$`)
	for name, code := range files {
		ext := path.Ext(name)
		if strings.HasPrefix(path.Base(name), "__krun_") || !valid.MatchString(name) || path.Clean(name) != name || strings.HasPrefix(name, "/") || strings.HasPrefix(name, "../") || len(name) > 160 || !contains([]string{".js", ".mjs", ".ts", ".json"}, ext) {
			return problem(422, "WORKER_SOURCE_INVALID", "Only safe relative JS, TS and JSON paths are supported")
		}
		total += len(code)
	}
	if total > 256<<10 {
		return problem(413, "WORKER_SOURCE_INVALID", "Source exceeds 256 KiB")
	}
	if _, ok := files[entry]; !ok || !contains([]string{".js", ".mjs", ".ts"}, path.Ext(entry)) {
		return problem(422, "WORKER_SOURCE_INVALID", "Entrypoint must be an included JS or TS file")
	}
	return nil
}
func workerVariablesValid(vars map[string]string) bool {
	if len(vars) > 32 {
		return false
	}
	size := 0
	re := regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
	for k, v := range vars {
		if !re.MatchString(k) || strings.HasPrefix(k, "KRUN_") {
			return false
		}
		size += len(v)
	}
	return size <= 16<<10
}
