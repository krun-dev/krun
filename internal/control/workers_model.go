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
	// BuilderMode selects the build executor: "kubernetes" creates a Job;
	// "local" runs workers/builder/build-worker.mjs on the control-plane host.
	BuilderMode    string `json:"builder_mode"`
	BuilderScript  string `json:"builder_script"`
	RuntimeService string `json:"runtime_service"`
	// Kubernetes connection settings are regular application configuration.
	// They intentionally live here instead of being read from hidden process
	// environment variables so every runtime input passes through LoadConfig.
	KubernetesAPI       string `json:"kubernetes_api"`
	KubernetesTokenFile string `json:"kubernetes_token_file"`
	KubernetesCAFile    string `json:"kubernetes_ca_file"`
	// Gateways and Domains are the routing catalog. Neither carries a network
	// classification: a domain is just a suffix plus which Gateway instance
	// serves it. Label is display text and never participates in routing.
	Gateways              []WorkerGateway `json:"gateways"`
	Domains               []WorkerDomain  `json:"domains"`
	RuntimeToken          string          `json:"runtime_token"`
	ManagementToken       string          `json:"management_token"`
	RuntimeManagementBase string          `json:"runtime_management_base"`
	S3Endpoint            string          `json:"s3_endpoint"`
	S3BuilderEndpoint     string          `json:"s3_builder_endpoint"`
	S3RuntimeEndpoint     string          `json:"s3_runtime_endpoint"`
	S3Bucket              string          `json:"s3_bucket"`
	S3LocationID          string          `json:"s3_location_id"`
	S3AccessKey           string          `json:"s3_access_key"`
	S3SecretKey           string          `json:"s3_secret_key"`
	PreviewLimit          int             `json:"preview_limit"`
	StaticService         string          `json:"static_service"`
	StaticServicePort     int             `json:"static_service_port"`
	// ConfigBaseURL is the runtime-readable origin of config/latest.json and
	// config/snapshots/*. It must be a private or cluster-internal endpoint.
	ConfigBaseURL string `json:"config_base_url"`
	// CloneDirectory is where repository checkouts are written during a build.
	// Required when the control plane runs with a read-only root filesystem.
	CloneDirectory string `json:"clone_directory"`
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

// WorkerDomain is one browser-facing suffix users can attach to an environment.
// Label is purely descriptive ("公网", "内网", "办公网"); routing depends only on
// Domain and Gateway.
type WorkerDomain struct {
	ID      string `json:"id"`
	Domain  string `json:"domain"`
	Label   string `json:"label"`
	Gateway string `json:"gateway"`
	// Default selects this domain for newly created environments.
	Default bool `json:"default"`
	// StaticDomain overrides the static-asset host served through this domain's
	// Gateway. Empty derives "static." + Domain.
	StaticDomain string `json:"static_domain"`
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
	if c.BuilderMode == "" {
		c.BuilderMode = "kubernetes"
	}
	if c.BuilderMode != "kubernetes" && c.BuilderMode != "local" {
		return fmt.Errorf("invalid Workers builder mode")
	}
	if c.BuilderMode == "local" && c.BuilderScript == "" {
		c.BuilderScript = "workers/builder/build-worker.mjs"
	}
	if c.S3LocationID == "" {
		c.S3LocationID = "primary"
	}
	if c.RuntimeService == "" {
		c.RuntimeService = "runtime-default"
	}
	if c.KubernetesAPI == "" {
		c.KubernetesAPI = "https://kubernetes.default.svc"
	}
	u, e := url.Parse(c.KubernetesAPI)
	if e != nil || u.Scheme != "https" || u.Host == "" || (u.Path != "" && u.Path != "/") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("invalid Workers Kubernetes API")
	}
	c.KubernetesAPI = strings.TrimSuffix(c.KubernetesAPI, "/")
	if c.KubernetesTokenFile == "" {
		c.KubernetesTokenFile = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	}
	if c.KubernetesCAFile == "" {
		c.KubernetesCAFile = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
	}
	if !path.IsAbs(c.KubernetesTokenFile) || !path.IsAbs(c.KubernetesCAFile) {
		return fmt.Errorf("Workers Kubernetes credential files must be absolute paths")
	}
	dns := regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,180}$`)
	if e := c.validateCatalog(dns); e != nil {
		return e
	}
	u, e = url.Parse(c.S3Endpoint)
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
	if c.S3RuntimeEndpoint != "" {
		u, e := url.Parse(c.S3RuntimeEndpoint)
		if e != nil || u.Host == "" || u.Path != "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
			return fmt.Errorf("invalid Workers S3 runtime endpoint")
		}
	} else {
		// Existing deployments used the builder-visible endpoint for runtime
		// downloads as well. Keep that default while allowing local-first to
		// give an off-host runtime a distinct, reachable origin.
		c.S3RuntimeEndpoint = c.S3BuilderEndpoint
	}
	if (c.BuilderMode != "local" && c.BuilderImage == "") || len(c.RuntimeToken) < 24 || len(c.ManagementToken) < 24 || c.S3AccessKey == "" || c.S3SecretKey == "" {
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
	if c.StaticService == "" {
		c.StaticService = "static-assets"
	}
	if c.StaticServicePort == 0 {
		c.StaticServicePort = 9000
	}
	if !dns.MatchString(c.StaticService) {
		return fmt.Errorf("invalid Workers static service")
	}
	if c.CloneDirectory != "" && !path.IsAbs(c.CloneDirectory) {
		return fmt.Errorf("clone_directory must be an absolute path")
	}
	if c.ConfigBaseURL == "" {
		c.ConfigBaseURL = strings.TrimSuffix(c.S3Endpoint, "/") + "/" + c.S3Bucket + "/config"
	}
	u, e = url.Parse(c.ConfigBaseURL)
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("invalid Workers config base URL")
	}
	c.ConfigBaseURL = strings.TrimSuffix(c.ConfigBaseURL, "/")
	return nil
}

// validateCatalog normalises and checks the Gateway and domain lists. Gateways
// come first so domains can reference them by id.
func (c *WorkersConfig) validateCatalog(dns *regexp.Regexp) error {
	if len(c.Gateways) == 0 {
		return fmt.Errorf("Workers gateways required")
	}
	seenGateway := map[string]bool{}
	for i := range c.Gateways {
		g := &c.Gateways[i]
		if g.ID == "" {
			return fmt.Errorf("Workers gateway id required")
		}
		if seenGateway[g.ID] {
			return fmt.Errorf("duplicate Workers gateway id: %s", g.ID)
		}
		seenGateway[g.ID] = true
		if g.Namespace == "" {
			g.Namespace = "kgateway-system"
		}
		if g.Section == "" {
			g.Section = g.ID
		}
		for _, v := range []string{g.Name, g.Namespace, g.Section} {
			if !dns.MatchString(v) {
				return fmt.Errorf("invalid Workers gateway %s", g.ID)
			}
		}
		if g.Base != "" {
			u, e := url.Parse(g.Base)
			if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
				return fmt.Errorf("invalid Workers gateway base for %s", g.ID)
			}
			g.Base = strings.TrimSuffix(g.Base, "/")
		}
	}
	if len(c.Domains) == 0 {
		return fmt.Errorf("Workers domains required")
	}
	seenDomain, seenID := map[string]bool{}, map[string]bool{}
	for i := range c.Domains {
		d := &c.Domains[i]
		if d.ID == "" {
			return fmt.Errorf("Workers domain id required")
		}
		if seenID[d.ID] {
			return fmt.Errorf("duplicate Workers domain id: %s", d.ID)
		}
		seenID[d.ID] = true
		if !dns.MatchString(d.Domain) {
			return fmt.Errorf("invalid Workers domain: %s", d.Domain)
		}
		if d.Domain != strings.ToLower(d.Domain) {
			return fmt.Errorf("invalid Workers domain: %s", d.Domain)
		}
		if seenDomain[d.Domain] {
			return fmt.Errorf("duplicate Workers domain: %s", d.Domain)
		}
		seenDomain[d.Domain] = true
		if !seenGateway[d.Gateway] {
			return fmt.Errorf("Workers domain %s references unknown gateway %q", d.ID, d.Gateway)
		}
		if d.StaticDomain == "" {
			d.StaticDomain = "static." + d.Domain
		}
		if !dns.MatchString(d.StaticDomain) {
			return fmt.Errorf("invalid Workers static domain for %s", d.ID)
		}
	}
	if len(c.Domains) == 1 && !c.Domains[0].Default {
		c.Domains[0].Default = true
	}
	return nil
}

// gateway returns the configured Gateway instance by id.
func (c *WorkersConfig) gateway(id string) (WorkerGateway, bool) {
	if c == nil {
		return WorkerGateway{}, false
	}
	for _, g := range c.Gateways {
		if g.ID == id {
			return g, true
		}
	}
	return WorkerGateway{}, false
}

// domain returns the configured domain entry by id.
func (c *WorkersConfig) domain(id string) (WorkerDomain, bool) {
	if c == nil {
		return WorkerDomain{}, false
	}
	for _, d := range c.Domains {
		if d.ID == id {
			return d, true
		}
	}
	return WorkerDomain{}, false
}

// domainForHost resolves which configured domain a route host belongs to. The
// longest matching suffix wins so overlapping suffixes stay unambiguous.
func (c *WorkersConfig) domainForHost(host string) (WorkerDomain, bool) {
	if c == nil || host == "" {
		return WorkerDomain{}, false
	}
	var best WorkerDomain
	found := false
	bestLen := 0
	for _, d := range c.Domains {
		if strings.HasSuffix(host, "."+d.Domain) && len(d.Domain) > bestLen {
			best, bestLen, found = d, len(d.Domain), true
		}
	}
	return best, found
}

// defaultDomains returns the ids newly created environments start with.
func (c *WorkersConfig) defaultDomains() []string {
	out := []string{}
	if c == nil {
		return out
	}
	for _, d := range c.Domains {
		if d.Default {
			out = append(out, d.ID)
		}
	}
	if len(out) == 0 && len(c.Domains) > 0 {
		out = append(out, c.Domains[0].ID)
	}
	return out
}

// workerSlug is the immutable, non-enumerable service label. It is always
// "w-" plus 16 hex characters, so a host can be split into slug and
// environment without ambiguity: the first '-' after position 18 starts the
// environment name.
func workerSlug(organization, service string) string {
	return "w-" + digest(organization + "/" + service)[:16]
}

// hostFor derives the route host for one environment under one domain suffix.
// Every domain of an environment shares the same label, so a service keeps one
// identity; only the DNS suffix differs, and the suffix decides which Gateway
// instance serves the host.
func hostFor(slug, env, suffix string) string {
	if slug == "" || env == "" || suffix == "" {
		return ""
	}
	return slug + "-" + env + "." + suffix
}

// gatewayBase returns the address console invoke uses for a Gateway instance.
// An explicit base (SSH tunnel) wins; otherwise the in-cluster service is used.
func (g WorkerGateway) gatewayBase() string {
	if g.Base != "" {
		return g.Base
	}
	return ""
}

func (g WorkerGateway) inClusterAddress() string {
	return g.Name + "." + g.Namespace + ".svc:8080"
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
	// UseRevision arbitrates build reservations against object-store GC. It is
	// storage coordination metadata and is never exposed by the API.
	UseRevision int64 `json:"-"`
}
type WorkerEnvironment struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	// Domains mirrors the service-level selection, in catalog order. Routing
	// decisions live on the service; this copy keeps each environment row
	// self-describing for projections and API responses.
	Domains []string `json:"domains"`
	// Hosts holds one route host per selected domain: {slug}-{env}.{domain}.
	// Disabled environments keep their hosts so the runtime can answer 410.
	Hosts []string `json:"hosts"`
	// DisplayHosts maps domain id to the browser-facing form of its host.
	// Presentation only: routing uses Hosts.
	DisplayHosts   map[string]string `json:"display_hosts,omitempty"`
	ConfigRevision int               `json:"configuration_revision"`
	Variables      map[string]string `json:"variables"`
	Revision       int               `json:"release_revision"`
	Release        string            `json:"release_id,omitempty"`
	Confirmed      string            `json:"confirmed_release_id,omitempty"`
}
type WorkerService struct {
	Repository         *WorkerRepositorySource `json:"repository_source,omitempty"`
	RepositoryRevision int                     `json:"repository_revision"`
	// DomainsRevision is the optimistic-concurrency token for replacing the
	// service's routing domains. PreviewRevision is internal coordination for
	// changes to the preview-environment set; it prevents concurrent creates
	// from both passing the configured preview limit.
	DomainsRevision int    `json:"domains_revision"`
	PreviewRevision int    `json:"preview_revision"`
	Service         string `json:"service_id"`
	Organization    string `json:"organization_id"`
	// Domains is the service-level domain selection. Every environment of the
	// service is served on every selected domain, so adding a domain adds one
	// host per environment without touching per-environment configuration.
	Domains      []string                     `json:"domains"`
	Environments map[string]WorkerEnvironment `json:"environments"`
}

// syncEnvironmentHosts recomputes an environment's Hosts from the service's
// domain selection. Every write path funnels through it so persisted hosts, the
// edge projection and the HTTPRoutes cannot drift apart. Domains that no longer
// exist in the catalog are dropped.
func (s *Server) syncEnvironmentHosts(env *WorkerEnvironment, organization, service string, serviceDomains []string) {
	if s.Config.Workers == nil || env == nil {
		return
	}
	c := s.Config.Workers
	slug := workerSlug(organization, service)
	selected := map[string]bool{}
	for _, id := range serviceDomains {
		selected[id] = true
	}
	// Emit in catalog order so Domains[i] and Hosts[i] stay aligned for clients.
	env.Domains = make([]string, 0, len(selected))
	env.Hosts = make([]string, 0, len(selected))
	env.DisplayHosts = map[string]string{}
	for _, d := range c.Domains {
		if !selected[d.ID] {
			continue
		}
		host := hostFor(slug, env.Name, d.Domain)
		if host == "" {
			continue
		}
		env.Domains = append(env.Domains, d.ID)
		env.Hosts = append(env.Hosts, host)
		env.DisplayHosts[d.ID] = host
	}
}

// syncServiceHosts applies the service's domain selection to every environment
// and returns the normalised (catalog-ordered) selection.
func (s *Server) syncServiceHosts(ws *WorkerService) []string {
	if s.Config.Workers == nil || ws == nil {
		return nil
	}
	ws.Domains = s.syncServiceDomains(*ws)
	for name, env := range ws.Environments {
		s.syncEnvironmentHosts(&env, ws.Organization, ws.Service, ws.Domains)
		ws.Environments[name] = env
	}
	return ws.Domains
}

// syncServiceDomains returns the selection limited to catalog entries, in
// catalog order, without touching any environment. Read endpoints use it so a
// GET never rewrites a service row.
func (s *Server) syncServiceDomains(ws WorkerService) []string {
	if s.Config.Workers == nil {
		return ws.Domains
	}
	selected := map[string]bool{}
	for _, id := range ws.Domains {
		selected[id] = true
	}
	kept := make([]string, 0, len(selected))
	for _, d := range s.Config.Workers.Domains {
		if selected[d.ID] {
			kept = append(kept, d.ID)
		}
	}
	if len(kept) == 0 {
		return s.Config.Workers.defaultDomains()
	}
	return kept
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
	EdgeRevision     int          `json:"edge_revision,omitempty"`
	ReleaseRevision  int          `json:"release_revision,omitempty"`
	Error            string       `json:"error,omitempty"`
	Steps            []WorkerStep `json:"steps"`
	Created          time.Time    `json:"created_at"`
	Finished         *time.Time   `json:"finished_at,omitempty"`
}
type WorkerOperationPrivate struct{ Actor WorkerActor }
type WorkerHistory struct {
	Revision  int       `json:"revision"`
	Release   string    `json:"release_id"`
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
}

func newWorkerState() *WorkerState {
	return &WorkerState{Revision: 1, Services: map[string]WorkerService{}, Builds: map[string]WorkerBuild{}, BuildPrivate: map[string]WorkerBuildPrivate{}, Artifacts: map[string]WorkerArtifact{}, Releases: map[string]WorkerRelease{}, Operations: map[string]WorkerOperation{}, OperationPrivate: map[string]WorkerOperationPrivate{}, Histories: map[string][]WorkerHistory{}, Idempotency: map[string]WorkerIdempotency{}}
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
	return sourceEntryValid(entry, files)
}

// sourceEntryValid checks just the entrypoint. Repository builds resolve the
// entrypoint from wrangler config after the tree is fetched, so the file-level
// checks and the entrypoint check happen at different times.
func sourceEntryValid(entry string, files map[string]string) error {
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
