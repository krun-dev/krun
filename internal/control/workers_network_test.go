package control

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSameWorkerPlacementUsesInstantEquality(t *testing.T) {
	created := time.Date(2026, time.October, 29, 3, 4, 5, 6, time.FixedZone("test", 8*60*60))
	placement := WorkerPlacement{Service: "service", Organization: "org", NetworkProfile: "default", Pool: "pool", Role: "primary", State: "active", Revision: 1, Created: created, Updated: created}
	other := placement
	other.Created, other.Updated = created.UTC(), created.UTC()
	if !sameWorkerPlacement(placement, other) || !sameWorkerPlacements(map[string]WorkerPlacement{"one": placement}, map[string]WorkerPlacement{"one": other}) {
		t.Fatal("equal placement instants with different locations were treated as different metadata")
	}
	other.Revision++
	if sameWorkerPlacement(placement, other) {
		t.Fatal("changed placement metadata was treated as equal")
	}
}

// catalogConfig models the current deployment: two Gateway instances and two
// domains whose labels are purely descriptive.
func catalogConfig() *WorkersConfig {
	return &WorkersConfig{
		Namespace:     "workers-mvp",
		BuilderMode:   "local",
		BuilderScript: "workers/builder/build-worker.mjs",
		RuntimePools: []WorkerRuntimePool{{
			ID: "default", Access: []string{"public", "private"}, PrivateEgress: "direct", PublicEgress: "direct",
			RuntimeService: "runtime-default", RuntimeToken: strings.Repeat("r", 24),
			ManagementToken: strings.Repeat("m", 24), Default: true,
		}},
		NetworkProfiles: []WorkerNetworkProfile{{
			ID: "default", Name: "Default", Default: true, Approval: "none", Visibility: "public",
			Requirements: WorkerNetworkRequirements{Ingress: []string{"public", "private"}, PrivateEgress: "direct", PublicEgress: "direct"},
		}},
		S3Endpoint:  "http://127.0.0.1:9000",
		S3Bucket:    "krun-artifacts",
		S3AccessKey: "access",
		S3SecretKey: "secret",
		Gateways: []WorkerGateway{
			{ID: "internal", Name: "workers-mvp", Namespace: "kgateway-system", Section: "private", Base: "http://127.0.0.1:38080"},
			{ID: "external", Name: "workers-public", Namespace: "kgateway-system", Section: "public", Base: "http://127.0.0.1:38081"},
		},
		Domains: []WorkerDomain{
			{ID: "internal", Domain: "workers.test", Label: "办公网络", Access: "private", Gateway: "internal", Labels: map[string]string{"exposure": "internal"}, Default: true},
			{ID: "external", Domain: "workers-out.example.test", Label: "互联网", Access: "public", Gateway: "external", Labels: map[string]string{"exposure": "external"}},
		},
	}
}

func TestCatalogValidationAndDefaults(t *testing.T) {
	c := catalogConfig()
	if err := c.validate(); err != nil {
		t.Fatal(err)
	}
	if c.Domains[0].StaticDomain != "static.workers.test" {
		t.Fatalf("static domain default: %q", c.Domains[0].StaticDomain)
	}
	if c.S3BuilderEndpoint != c.S3Endpoint || c.S3RuntimeEndpoint != c.S3BuilderEndpoint {
		t.Fatalf("storage endpoint defaults: control=%q builder=%q runtime=%q", c.S3Endpoint, c.S3BuilderEndpoint, c.S3RuntimeEndpoint)
	}
	if c.NPMRegistry != "https://registry.npmjs.org/" {
		t.Fatalf("npm registry default: %q", c.NPMRegistry)
	}
	if c.BuilderTimeoutSeconds != 600 {
		t.Fatalf("builder timeout default: %d", c.BuilderTimeoutSeconds)
	}
	if got := c.defaultDomains(); len(got) != 1 || got[0] != "internal" {
		t.Fatalf("default domains: %v", got)
	}
	// Labels are inert: routing only depends on the Gateway binding.
	c.Domains[1].Label = "办公网"
	if err := c.validate(); err != nil {
		t.Fatal(err)
	}
	if d, ok := c.domainForHost("w-abc-production.workers-out.example.test"); !ok || d.ID != "external" {
		t.Fatalf("host must resolve to the external domain: %+v", d)
	}
}

func multiPoolCatalogConfig() *WorkersConfig {
	c := catalogConfig()
	c.RuntimePools = []WorkerRuntimePool{
		{ID: "private-a", Access: []string{"private"}, Labels: map[string]string{"zone": "internal"}, RuntimeService: "runtime-private", RuntimeToken: strings.Repeat("p", 24), ManagementToken: strings.Repeat("q", 24), Default: true},
		{ID: "public-b", Access: []string{"public"}, Labels: map[string]string{"zone": "external"}, RuntimeService: "runtime-public", RuntimeToken: strings.Repeat("u", 24), ManagementToken: strings.Repeat("v", 24)},
	}
	c.NetworkProfiles = []WorkerNetworkProfile{
		{ID: "internal-service", Name: "Internal service", Description: "Reachable from the deployment's internal network.", DomainSelector: map[string]string{"exposure": "internal"}, PoolSelector: map[string]string{"zone": "internal"}, Requirements: WorkerNetworkRequirements{Ingress: []string{"private"}, PrivateEgress: "direct", PublicEgress: "deny"}, Default: true},
		{ID: "external-service", Name: "External service", Description: "Reachable through the deployment's external entry.", DomainSelector: map[string]string{"exposure": "external"}, PoolSelector: map[string]string{"zone": "external"}, Requirements: WorkerNetworkRequirements{Ingress: []string{"public"}, PrivateEgress: "deny", PublicEgress: "direct"}},
	}
	return c
}

func TestRuntimePoolsExposeOnlyProductNetworkProfiles(t *testing.T) {
	c := multiPoolCatalogConfig()
	if err := c.validate(); err != nil {
		t.Fatal(err)
	}
	s, store, base := workerTestServer(t)
	s.Config.Workers = c
	s.routes()

	profiles := request(s, http.MethodGet, "/api/v1/orgs/org-a/workers/network-profiles", "krun_cli_admin", "")
	if profiles.Code != http.StatusOK || strings.Contains(profiles.Body.String(), "private-a") || strings.Contains(profiles.Body.String(), "runtime-private") || strings.Contains(profiles.Body.String(), "private_egress") || strings.Contains(profiles.Body.String(), "pool_selector") {
		t.Fatalf("internal placement leaked through network profiles: %d %s", profiles.Code, profiles.Body.String())
	}
	var response struct {
		Items []WorkerNetworkProfileView `json:"items"`
	}
	if err := json.Unmarshal(profiles.Body.Bytes(), &response); err != nil || len(response.Items) != 2 || response.Items[0].ID != "internal-service" || response.Items[1].ID != "external-service" {
		t.Fatalf("product profiles: %+v err=%v", response.Items, err)
	}

	if rejected := request(s, http.MethodPut, base+"/domains", "krun_cli_admin", `{"domains":["external"],"expected_revision":0}`); rejected.Code != http.StatusUnprocessableEntity {
		t.Fatalf("domain outside current profile accepted: %d %s", rejected.Code, rejected.Body.String())
	}
	changed := request(s, http.MethodPut, base+"/network-profile", "krun_cli_admin", `{"network_profile":"external-service","expected_revision":0}`)
	if changed.Code != http.StatusOK || strings.Contains(changed.Body.String(), "public-b") || strings.Contains(changed.Body.String(), `"access"`) || strings.Contains(changed.Body.String(), "internal") {
		t.Fatalf("external profile selection: %d %s", changed.Code, changed.Body.String())
	}
	if err := store.View(context.Background(), PartitionWorkers, func(st *State) error {
		placement, ok := activeWorkerPlacement(st.Workers, "svc-a")
		if got := placement.Pool; !ok || got != "public-b" {
			t.Fatalf("hidden pool assignment=%q", got)
		}
		if got := st.Workers.Services["svc-a"].NetworkProfile; got != "external-service" {
			t.Fatalf("saved network profile=%q", got)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	unsupported := request(s, http.MethodPut, base+"/domains", "krun_cli_admin", `{"domains":["internal","external"],"expected_revision":1}`)
	if unsupported.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unsupported combination accepted: %d %s", unsupported.Code, unsupported.Body.String())
	}
}

func TestRuntimePoolsAllowMultiplePlacementsPerProfile(t *testing.T) {
	c := multiPoolCatalogConfig()
	c.RuntimePools = append(c.RuntimePools, WorkerRuntimePool{ID: "public-c", Access: []string{"public"}, Labels: map[string]string{"zone": "external"}, RuntimeService: "runtime-public-2", RuntimeToken: strings.Repeat("x", 24), ManagementToken: strings.Repeat("y", 24)})
	if err := c.validate(); err != nil {
		t.Fatalf("multiple compatible runtime pools rejected: %v", err)
	}
	first, ok := c.selectPoolForService("external-service", "service-a")
	second, okAgain := c.selectPoolForService("external-service", "service-a")
	if !ok || !okAgain || first.ID != second.ID || (first.ID != "public-b" && first.ID != "public-c") {
		t.Fatalf("unstable profile placement: first=%+v second=%+v", first, second)
	}
}

func TestNetworkProfileApprovalAndPublishedPlacementLock(t *testing.T) {
	t.Run("approval", func(t *testing.T) {
		c := multiPoolCatalogConfig()
		c.NetworkProfiles[1].Approval = "required"
		if err := c.validate(); err != nil {
			t.Fatal(err)
		}
		s, _, base := workerTestServer(t)
		s.Config.Workers = c
		s.routes()

		profiles := request(s, http.MethodGet, "/api/v1/orgs/org-a/workers/network-profiles", "krun_cli_admin", "")
		if profiles.Code != http.StatusOK || !strings.Contains(profiles.Body.String(), `"approval_required":true`) {
			t.Fatalf("approval metadata missing: %d %s", profiles.Code, profiles.Body.String())
		}
		changed := request(s, http.MethodPut, base+"/network-profile", "krun_cli_admin", `{"network_profile":"external-service","expected_revision":0}`)
		if changed.Code != http.StatusForbidden || !strings.Contains(changed.Body.String(), "NETWORK_PROFILE_APPROVAL_REQUIRED") {
			t.Fatalf("approval-gated profile selected directly: %d %s", changed.Code, changed.Body.String())
		}
	})

	t.Run("published", func(t *testing.T) {
		c := multiPoolCatalogConfig()
		if err := c.validate(); err != nil {
			t.Fatal(err)
		}
		s, store, base := workerTestServer(t)
		s.Config.Workers = c
		s.routes()
		if err := store.Update(context.Background(), PartitionWorkers, func(st *State) error {
			service := s.workerService(st, Target{ID: "svc-a", Organization: "org-a"})
			env := service.Environments[workerBaseline]
			env.Release = "release-existing"
			service.Environments[workerBaseline] = env
			st.Workers.Services[service.Service] = service
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		changed := request(s, http.MethodPut, base+"/network-profile", "krun_cli_admin", `{"network_profile":"external-service","expected_revision":0}`)
		if changed.Code != http.StatusConflict || !strings.Contains(changed.Body.String(), "WORKER_NETWORK_REASSIGNMENT_REQUIRED") {
			t.Fatalf("published placement changed without migration: %d %s", changed.Code, changed.Body.String())
		}
	})
}

func TestCatalogRejectsBrokenReferences(t *testing.T) {
	c := catalogConfig()
	c.Domains[1].Gateway = "missing"
	if err := c.validate(); err == nil {
		t.Fatal("domain referencing an unknown gateway accepted")
	}
	c = catalogConfig()
	c.Domains[1].Domain = c.Domains[0].Domain
	if err := c.validate(); err == nil {
		t.Fatal("duplicate domain accepted")
	}
	c = catalogConfig()
	c.Domains[1].Access = "partner"
	if err := c.validate(); err == nil {
		t.Fatal("unknown portable access class accepted")
	}
	c = catalogConfig()
	c.Gateways = nil
	if err := c.validate(); err == nil {
		t.Fatal("empty gateway list accepted")
	}
	c = catalogConfig()
	c.Domains = nil
	if err := c.validate(); err == nil {
		t.Fatal("empty domain list accepted")
	}
}

func TestRuntimeStorageEndpointValidation(t *testing.T) {
	c := catalogConfig()
	c.S3BuilderEndpoint = "http://builder.storage.test:9000"
	c.S3RuntimeEndpoint = "http://static-assets.workers-mvp.svc:9000"
	if err := c.validate(); err != nil {
		t.Fatal(err)
	}
	if c.S3RuntimeEndpoint != "http://static-assets.workers-mvp.svc:9000" {
		t.Fatalf("runtime endpoint changed: %q", c.S3RuntimeEndpoint)
	}

	c = catalogConfig()
	c.S3RuntimeEndpoint = "file:///tmp/storage"
	if err := c.validate(); err == nil || !strings.Contains(err.Error(), "runtime endpoint") {
		t.Fatalf("invalid runtime endpoint accepted: %v", err)
	}
}

func TestNPMRegistryValidation(t *testing.T) {
	c := catalogConfig()
	c.NPMRegistry = "https://npm.example.test/company"
	if err := c.validate(); err != nil || c.NPMRegistry != "https://npm.example.test/company/" {
		t.Fatalf("registry normalization: %q %v", c.NPMRegistry, err)
	}
	for _, value := range []string{"http://npm.example.test/", "https://user:secret@npm.example.test/", "https://npm.example.test/?token=secret"} {
		c = catalogConfig()
		c.NPMRegistry = value
		if err := c.validate(); err == nil {
			t.Fatalf("invalid npm registry accepted: %s", value)
		}
	}
}

func TestBuilderIsolationConfigurationValidation(t *testing.T) {
	c := catalogConfig()
	c.BuilderRuntimeClass = "gvisor.production"
	c.BuilderTimeoutSeconds = 900
	if err := c.validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*WorkersConfig){
		func(c *WorkersConfig) { c.BuilderRuntimeClass = "Bad_Runtime_Class" },
		func(c *WorkersConfig) { c.BuilderTimeoutSeconds = 59 },
		func(c *WorkersConfig) { c.BuilderTimeoutSeconds = 1801 },
	} {
		c = catalogConfig()
		mutate(c)
		if err := c.validate(); err == nil {
			t.Fatal("invalid builder isolation configuration accepted")
		}
	}
}

func captureBuildJob(t *testing.T, build WorkerBuild) (map[string]any, map[string]any) {
	t.Helper()
	var submitted map[string]any
	var inputSecret map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/jobs/"):
			http.NotFound(w, r)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/secrets"):
			if err := json.NewDecoder(r.Body).Decode(&inputSecret); err != nil {
				t.Fatal(err)
			}
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPatch && strings.Contains(r.URL.Path, "/secrets/"):
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPatch && strings.Contains(r.URL.Path, "/jobs/"):
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/jobs"):
			if err := json.NewDecoder(r.Body).Decode(&submitted); err != nil {
				t.Fatal(err)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"metadata":{"uid":"job-uid"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	k := &workerKubernetes{base: srv.URL, namespace: "workers-mvp", npmRegistry: "https://npm.example.test/", client: srv.Client()}
	if err := k.ensureBuildJob(context.Background(), build, WorkerBuildPrivate{}, "https://source.example.test/source", "https://storage.example.test/upload", "builder:test", "Never"); err != nil {
		t.Fatal(err)
	}
	return submitted, inputSecret
}

func TestBuildJobBoundsEphemeralStorage(t *testing.T) {
	build := WorkerBuild{ID: "build", Job: "krun-build-direct", Entrypoint: "index.js", CompatibilityDate: workerDefaultCompatibilityDate}
	submitted, inputSecret := captureBuildJob(t, build)

	jobSpec := submitted["spec"].(map[string]any)
	template := jobSpec["template"].(map[string]any)
	podSpec := template["spec"].(map[string]any)
	if _, exists := podSpec["initContainers"]; exists {
		t.Fatal("direct build unexpectedly uses a repository build init container")
	}
	container := podSpec["containers"].([]any)[0].(map[string]any)
	resources := container["resources"].(map[string]any)
	requests := resources["requests"].(map[string]any)
	limits := resources["limits"].(map[string]any)
	if requests["ephemeral-storage"] != workerBuilderEphemeralRequest || limits["ephemeral-storage"] != workerBuilderEphemeralLimit {
		t.Fatalf("ephemeral storage resources: requests=%v limits=%v", requests, limits)
	}

	sizes := map[string]string{}
	for _, raw := range podSpec["volumes"].([]any) {
		volume := raw.(map[string]any)
		if empty, ok := volume["emptyDir"].(map[string]any); ok {
			sizes[volume["name"].(string)], _ = empty["sizeLimit"].(string)
		}
	}
	if sizes["work"] != workerBuilderWorkSize || sizes["build-tmp"] != workerBuilderTmpSize {
		t.Fatalf("emptyDir size limits: %v", sizes)
	}
	data := inputSecret["stringData"].(map[string]any)
	if len(data) != 1 {
		t.Fatalf("direct build should receive one input: %#v", data)
	}
	directInput := data["input.json"].(string)
	if !strings.Contains(directInput, "source_url") || !strings.Contains(directInput, "upload_url") {
		t.Fatalf("direct build input is incomplete: %s", directInput)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(directInput), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["build_mode"] != "direct" || decoded["entrypoint"] != "index.js" {
		t.Fatalf("direct build contract: %#v", decoded)
	}
}

func TestRepositoryBuildJobSeparatesBuildAndPackCapabilities(t *testing.T) {
	build := WorkerBuild{
		ID:                "build",
		Job:               "krun-build-repository",
		Entrypoint:        "index.js",
		CompatibilityDate: workerDefaultCompatibilityDate,
		Repository:        &WorkerBuildRepository{ConfigPath: "wrangler.jsonc"},
		// Repository configuration, not a stale service snapshot, is
		// authoritative for assets.
		Assets: &WorkerAssetsConfig{Directory: "old-public"},
	}
	submitted, inputSecret := captureBuildJob(t, build)
	jobSpec := submitted["spec"].(map[string]any)
	template := jobSpec["template"].(map[string]any)
	podSpec := template["spec"].(map[string]any)
	if len(podSpec["initContainers"].([]any)) != 1 || len(podSpec["containers"].([]any)) != 1 {
		t.Fatalf("repository build must have one build and one pack container: %#v", podSpec)
	}

	data := inputSecret["stringData"].(map[string]any)
	buildInput := data["build.json"].(string)
	packInput := data["pack.json"].(string)
	if strings.Contains(buildInput, "upload") || !strings.Contains(buildInput, "source_url") {
		t.Fatalf("untrusted build input leaks upload capability: %s", buildInput)
	}
	if strings.Contains(packInput, "source_url") || !strings.Contains(packInput, "upload_url") {
		t.Fatalf("trusted pack input has wrong capabilities: %s", packInput)
	}
	for name, raw := range map[string]string{"build": buildInput, "pack": packInput} {
		var decoded map[string]any
		if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
			t.Fatalf("decode %s input: %v", name, err)
		}
		if decoded["build_mode"] != "wrangler" || decoded["config_path"] != "wrangler.jsonc" {
			t.Fatalf("%s input lost repository build contract: %#v", name, decoded)
		}
		if name == "build" && decoded["assets"] != nil {
			t.Fatalf("repository build inherited stale assets: %#v", decoded["assets"])
		}
	}
}

func TestBuildJobCleansInputSecretWhenJobCreationFails(t *testing.T) {
	deleted := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/jobs/"):
			http.NotFound(w, r)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/secrets"):
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/jobs"):
			http.Error(w, "job rejected", http.StatusInternalServerError)
		case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/secrets/"):
			deleted = true
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	k := &workerKubernetes{base: srv.URL, namespace: "workers-mvp", client: srv.Client()}
	build := WorkerBuild{Job: "krun-build-cleanup", Entrypoint: "index.js", CompatibilityDate: workerDefaultCompatibilityDate}
	if err := k.ensureBuildJob(context.Background(), build, WorkerBuildPrivate{}, "https://source.test/source", "https://storage.test/upload", "builder:test", "Never"); err == nil {
		t.Fatal("job creation failure was ignored")
	}
	if !deleted {
		t.Fatal("ownerless build input Secret was not cleaned up")
	}
}

func TestBuildJobReattachesRecreatedInputSecret(t *testing.T) {
	secretPatched, jobPatched := false, false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/jobs/"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"metadata":{"uid":"job-uid","annotations":{"workers.krun.dev/input-adopted":"true"}}}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/secrets"):
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPatch && strings.Contains(r.URL.Path, "/secrets/"):
			secretPatched = true
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPatch && strings.Contains(r.URL.Path, "/jobs/"):
			jobPatched = true
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	k := &workerKubernetes{base: srv.URL, namespace: "workers-mvp", client: srv.Client()}
	build := WorkerBuild{Job: "krun-build-reattach", Entrypoint: "index.js", CompatibilityDate: workerDefaultCompatibilityDate}
	if err := k.ensureBuildJob(context.Background(), build, WorkerBuildPrivate{}, "https://source.test/source", "https://storage.test/upload", "builder:test", "Never"); err != nil {
		t.Fatal(err)
	}
	if !secretPatched || jobPatched {
		t.Fatalf("recreated Secret adoption: secretPatched=%v jobPatched=%v", secretPatched, jobPatched)
	}
}

func TestSyncEnvironmentHostsDerivesOneHostPerDomain(t *testing.T) {
	c := catalogConfig()
	if err := c.validate(); err != nil {
		t.Fatal(err)
	}
	s := &Server{Config: Config{Workers: c}}
	env := WorkerEnvironment{Name: "production"}
	s.syncEnvironmentHosts(&env, "org-a", "svc-a", []string{"internal", "external"})
	slug := workerSlug("org-a", "svc-a")
	want := []string{slug + "-production.workers.test", slug + "-production.workers-out.example.test"}
	sort.Strings(env.Hosts)
	sort.Strings(want)
	if strings.Join(env.Hosts, ",") != strings.Join(want, ",") {
		t.Fatalf("hosts: %v", env.Hosts)
	}
	if env.DisplayHosts["external"] != slug+"-production.workers-out.example.test" {
		t.Fatalf("per-domain host: %q", env.DisplayHosts["external"])
	}

	// A single domain yields exactly one host.
	one := WorkerEnvironment{Name: "fix-auth"}
	s.syncEnvironmentHosts(&one, "org-a", "svc-a", []string{"internal"})
	if len(one.Hosts) != 1 {
		t.Fatalf("single-domain environment: %v", one.Hosts)
	}

	// A domain removed from the catalog is dropped from the environment.
	gone := WorkerEnvironment{Name: "production"}
	s.syncEnvironmentHosts(&gone, "org-a", "svc-a", []string{"internal", "retired"})
	if len(gone.Domains) != 1 || gone.Domains[0] != "internal" {
		t.Fatalf("unknown domain retained: %v", gone.Domains)
	}
}

func TestServiceDomainsUpdateAddsAndRemovesHosts(t *testing.T) {
	s, store, base := workerTestServer(t)
	s.Config.Workers = catalogConfig()
	if err := s.Config.Workers.validate(); err != nil {
		t.Fatal(err)
	}
	s.routes()

	// Adding a domain at the service level adds one host to every environment.
	r := request(s, "PUT", base+"/domains", "krun_cli_admin", `{"domains":["internal","external"],"expected_revision":0}`)
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	var updated struct {
		Revision int `json:"revision"`
	}
	_ = json.Unmarshal(r.Body.Bytes(), &updated)
	if updated.Revision != 1 {
		t.Fatalf("domain revision after add: %d", updated.Revision)
	}
	envs := request(s, "GET", base+"/environments", "krun_cli_admin", "")
	var list struct {
		Items []WorkerEnvironment `json:"items"`
	}
	_ = json.Unmarshal(envs.Body.Bytes(), &list)
	for _, env := range list.Items {
		if len(env.Hosts) != 2 {
			t.Fatalf("%s: expected two hosts, got %v", env.Name, env.Hosts)
		}
	}

	// Removing it drops the host everywhere and prunes nothing else.
	r = request(s, "PUT", base+"/domains", "krun_cli_admin", `{"domains":["internal"],"expected_revision":1}`)
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	_ = store.Update(context.Background(), PartitionAll, func(st *State) error {
		ws := st.Workers.Services["svc-a"]
		if len(ws.Domains) != 1 || ws.Domains[0] != "internal" {
			t.Error("service domains:", ws.Domains)
		}
		for name, env := range ws.Environments {
			if len(env.Hosts) != 1 || !strings.HasSuffix(env.Hosts[0], ".workers.test") {
				t.Errorf("%s hosts: %v", name, env.Hosts)
			}
		}
		return nil
	})
}

func TestServiceDomainsRejectInvalidSelection(t *testing.T) {
	s, _, base := workerTestServer(t)
	s.Config.Workers = catalogConfig()
	if err := s.Config.Workers.validate(); err != nil {
		t.Fatal(err)
	}
	s.routes()
	cases := []string{
		`{"domains":[]}`,
		`{"domains":["nope"]}`,
		`{"domains":["internal","internal"]}`,
	}
	for _, body := range cases {
		if r := request(s, "PUT", base+"/domains", "krun_cli_admin", body); r.Code != 422 {
			t.Fatalf("%s accepted: %d %s", body, r.Code, r.Body.String())
		}
	}
}

func TestServiceDomainsReadReturnsCatalog(t *testing.T) {
	s, _, base := workerTestServer(t)
	s.Config.Workers = catalogConfig()
	if err := s.Config.Workers.validate(); err != nil {
		t.Fatal(err)
	}
	s.routes()
	r := request(s, "GET", base+"/domains", "krun_cli_viewer", "")
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	var out struct {
		Selected []string           `json:"selected"`
		Catalog  []WorkerDomainView `json:"catalog"`
		Revision int                `json:"revision"`
	}
	_ = json.Unmarshal(r.Body.Bytes(), &out)
	if len(out.Selected) != 1 || out.Selected[0] != "internal" {
		t.Fatalf("selected: %v", out.Selected)
	}
	if len(out.Catalog) != 2 {
		t.Fatalf("catalog: %+v", out.Catalog)
	}
	if out.Revision != 0 {
		t.Fatalf("revision: %d", out.Revision)
	}
}

func TestServiceDomainsRequiresCurrentRevision(t *testing.T) {
	s, _, base := workerTestServer(t)
	s.Config.Workers = catalogConfig()
	if err := s.Config.Workers.validate(); err != nil {
		t.Fatal(err)
	}
	s.routes()
	if r := request(s, "PUT", base+"/domains", "krun_cli_admin", `{"domains":["internal","external"]}`); r.Code != 422 {
		t.Fatalf("missing revision accepted: %d %s", r.Code, r.Body.String())
	}
	if r := request(s, "PUT", base+"/domains", "krun_cli_admin", `{"domains":["internal","external"],"expected_revision":9}`); r.Code != 409 {
		t.Fatalf("stale revision accepted: %d %s", r.Code, r.Body.String())
	}
}

func TestWorkerConfigProjectionCoversEveryDomain(t *testing.T) {
	st, _ := workerConfigFixture()
	ws := st.Workers.Services["svc-a"]
	ws.Domains = []string{"internal", "external"}
	st.Workers.Services["svc-a"] = ws
	p := workerConfigFromState(st, workerProjectionConfig([]WorkerDomain{
		{ID: "internal", Domain: "workers.test", Access: "private"},
		{ID: "external", Domain: "workers-out.example.test", Access: "public"},
	}))
	hosts := make([]string, 0, len(p.Routes))
	for _, route := range p.Routes {
		hosts = append(hosts, route.Host)
	}
	sort.Strings(hosts)
	slug := workerSlug("org-a", "svc-a")
	// Every selected domain yields one host per environment, in host order.
	want := []string{
		hostFor(slug, "empty", "workers-out.example.test"),
		hostFor(slug, "empty", "workers.test"),
		hostFor(slug, "preview", "workers-out.example.test"),
		hostFor(slug, "preview", "workers.test"),
		hostFor(slug, "production", "workers-out.example.test"),
		hostFor(slug, "production", "workers.test"),
	}
	if strings.Join(hosts, ",") != strings.Join(want, ",") {
		t.Fatalf("projection hosts: %v", hosts)
	}
}

func TestWorkerConfigProjectionRequiresDurablePlacement(t *testing.T) {
	st, _ := workerConfigFixture()
	service := st.Workers.Services["svc-a"]
	service.NetworkProfile = "default"
	service.Domains = []string{"external"}
	st.Workers.Services[service.Service] = service
	st.Workers.Placements = map[string]WorkerPlacement{}
	config := multiPoolCatalogConfig()
	if err := config.validate(); err != nil {
		t.Fatal(err)
	}

	projection := workerConfigFromState(st, config)
	if len(projection.Routes) != 0 || len(projection.Releases) != 0 {
		t.Fatalf("service without placement entered runtime projection: %+v", projection)
	}
}

// fakeKube records HTTPRoute writes so the domain-to-Gateway mapping can be
// asserted without a cluster.
type fakeKube struct {
	mu       sync.Mutex
	routes   map[string]map[string]any
	deleted  []string
	gateways map[string]bool
}

func (f *fakeKube) server(t *testing.T) *httptest.Server {
	t.Helper()
	if f.routes == nil {
		f.routes = map[string]map[string]any{}
		f.gateways = map[string]bool{}
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		path := r.URL.Path
		switch {
		case strings.Contains(path, "/gateways/"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"metadata": map[string]any{"generation": 1},
				"status": map[string]any{"conditions": []any{
					map[string]any{"type": "Programmed", "status": "True", "observedGeneration": 1},
				}},
			})
			f.gateways[path[strings.LastIndex(path, "/")+1:]] = true
			return
		case strings.HasSuffix(path, "/httproutes") && r.Method == http.MethodGet:
			items := make([]any, 0, len(f.routes))
			for name := range f.routes {
				items = append(items, map[string]any{"metadata": map[string]any{"name": name}})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
			return
		case strings.HasSuffix(path, "/httproutes") && r.Method == http.MethodPost:
			var body struct {
				Metadata struct {
					Name string `json:"name"`
				} `json:"metadata"`
				Spec map[string]any `json:"spec"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.routes[body.Metadata.Name] = body.Spec
			w.WriteHeader(http.StatusCreated)
			return
		case strings.Contains(path, "/httproutes/"):
			name := path[strings.LastIndex(path, "/")+1:]
			if r.Method == http.MethodDelete {
				f.deleted = append(f.deleted, name)
				delete(f.routes, name)
				w.WriteHeader(http.StatusOK)
				return
			}
			spec, ok := f.routes[name]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			if r.Method == http.MethodPatch {
				var body struct {
					Spec map[string]any `json:"spec"`
				}
				_ = json.NewDecoder(r.Body).Decode(&body)
				f.routes[name] = body.Spec
				spec = body.Spec
			}
			raw, _ := json.Marshal(spec)
			parent := spec["parentRefs"].([]any)[0].(map[string]any)
			gatewayName, _ := parent["name"].(string)
			gatewaySection, _ := parent["sectionName"].(string)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"metadata": map[string]any{"generation": 1},
				"spec":     json.RawMessage(raw),
				"status": map[string]any{"parents": []any{map[string]any{
					"parentRef":  map[string]any{"name": gatewayName, "namespace": "kgateway-system", "sectionName": gatewaySection},
					"conditions": []any{map[string]any{"type": "Accepted", "status": "True", "observedGeneration": 1}, map[string]any{"type": "ResolvedRefs", "status": "True", "observedGeneration": 1}},
				}}},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
}

func TestEnsureRoutesGroupsHostsByGateway(t *testing.T) {
	fake := &fakeKube{routes: map[string]map[string]any{}, gateways: map[string]bool{}}
	srv := fake.server(t)
	defer srv.Close()
	c := catalogConfig()
	if err := c.validate(); err != nil {
		t.Fatal(err)
	}
	k := &workerKubernetes{base: srv.URL, namespace: "workers-mvp", client: srv.Client()}
	runtimeService := "runtime-private"
	hosts := map[string][]string{
		"internal": {"w-1-production.workers.test"},
		"external": {"w-1-production.workers-out.example.test"},
	}
	if ready, err := k.ensureRoutes(context.Background(), c, "workers-abc", runtimeService, hosts); err != nil || ready {
		t.Fatalf("creation pass: %v ready=%v", err, ready)
	}
	if ready, err := k.ensureRoutes(context.Background(), c, "workers-abc", runtimeService, hosts); err != nil || !ready {
		t.Fatalf("ensureRoutes: %v ready=%v", err, ready)
	}
	// The first catalog Gateway keeps the base name; later ones are suffixed.
	internal, ok := fake.routes["workers-abc"]
	if !ok {
		t.Fatal("internal route missing")
	}
	external, ok := fake.routes["workers-abc-external"]
	if !ok {
		t.Fatal("external route missing")
	}
	ip := internal["parentRefs"].([]any)[0].(map[string]any)
	ep := external["parentRefs"].([]any)[0].(map[string]any)
	if ip["name"] != "workers-mvp" || ip["sectionName"] != "private" {
		t.Fatalf("internal route attached to %v", ip)
	}
	if ep["name"] != "workers-public" || ep["sectionName"] != "public" {
		t.Fatalf("external route attached to %v", ep)
	}
	for route, spec := range map[string]map[string]any{"internal": internal, "external": external} {
		rules := spec["rules"].([]any)
		backend := rules[0].(map[string]any)["backendRefs"].([]any)[0].(map[string]any)
		if backend["name"] != runtimeService {
			t.Fatalf("%s route backend=%v", route, backend)
		}
	}
	for _, raw := range external["hostnames"].([]any) {
		if strings.HasSuffix(raw.(string), "."+c.Domains[0].Domain) {
			t.Fatalf("internal host leaked onto the external route: %v", external["hostnames"])
		}
	}
}

func TestStaleWorkerRoutesPropagatesListFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	k := &workerKubernetes{base: srv.URL, namespace: "workers-mvp", client: srv.Client()}
	if _, err := staleWorkerRoutes(context.Background(), k, "workers-", map[string]bool{}); err == nil {
		t.Fatal("route list failure was hidden")
	}
}

func TestKubernetesClientReloadsRotatedBearerToken(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenPath, []byte("token-one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	seen := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	k := &workerKubernetes{base: srv.URL, tokenFile: tokenPath, namespace: "workers-mvp", client: srv.Client()}
	for _, token := range []string{"token-one", "token-two"} {
		if err := os.WriteFile(tokenPath, []byte(token), 0o600); err != nil {
			t.Fatal(err)
		}
		var response map[string]any
		if err := k.call(context.Background(), http.MethodGet, "/version", nil, &response); err != nil {
			t.Fatal(err)
		}
	}
	if len(seen) != 2 || seen[0] != "Bearer token-one" || seen[1] != "Bearer token-two" {
		t.Fatalf("authorization headers=%v", seen)
	}
}

func TestProcessOperationStopsAfterCommitCASConflict(t *testing.T) {
	s, store, _ := workerTestServer(t)
	raw := workerArchive(t, []string{"manifest.json", "modules/index.js"}, "export default {}", false)
	code, manifest, err := parseWorkerArchive(raw)
	if err != nil {
		t.Fatal(err)
	}
	s3Server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(raw)))
		w.Header().Set("ETag", `"archive"`)
		w.Header().Set("Last-Modified", now().UTC().Format(http.TimeFormat))
		if r.Method == http.MethodHead {
			return
		}
		_, _ = w.Write(raw)
	}))
	defer s3Server.Close()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/pods"):
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{map[string]any{
				"metadata": map[string]any{"name": "runtime-0", "uid": "pod-1"},
				"status":   map[string]any{"podIP": "127.0.0.1", "conditions": []any{map[string]any{"type": "Ready", "status": "True"}}},
			}}})
		case strings.HasSuffix(r.URL.Path, "/endpointslices"):
			ready := true
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{map[string]any{"endpoints": []any{map[string]any{
				"addresses": []string{"127.0.0.1"}, "conditions": map[string]any{"ready": ready}, "targetRef": map[string]any{"uid": "pod-1"},
			}}}}})
		case r.URL.Path == "/validate":
			_ = json.NewEncoder(w).Encode(map[string]any{"pod_uid": "pod-1", "release_id": "rel-cas", "archive_sha256": code.ArchiveDigest})
		default:
			http.NotFound(w, r)
		}
	}))
	defer apiServer.Close()

	c := testWorkersConfig()
	c.S3Endpoint, c.S3BuilderEndpoint = s3Server.URL, s3Server.URL
	c.S3Bucket, c.S3AccessKey, c.S3SecretKey, c.S3LocationID = "artifacts", "access", "secret", "test"
	c.RuntimePools[0].RuntimeManagementBase = apiServer.URL
	c.RuntimePools[0].ManagementToken = strings.Repeat("m", 24)
	s.Config.Workers = c
	s3, err := newWorkerS3(c)
	if err != nil {
		t.Fatal(err)
	}
	controller := &workersController{
		server: s, config: c, s3: s3, s3Builder: s3,
		kube: &workerKubernetes{base: apiServer.URL, namespace: c.Namespace, client: apiServer.Client()},
		http: apiServer.Client(), assetCache: map[string]workerCachedAssets{}, replica: "test",
	}
	release := WorkerRelease{ID: "rel-cas", Service: "svc-a", Organization: "org-a", Environment: "production", Artifact: "art-cas", ArchiveDigest: code.ArchiveDigest, ModuleDigest: code.Digest}
	artifact := WorkerArtifact{ID: "art-cas", Organization: "org-a", State: "finalized", Profile: code.Profile, ArchiveDigest: code.ArchiveDigest, ManifestDigest: manifest, ModuleDigest: code.Digest, CompatibilityDate: code.CompatibilityDate, CompatibilityFlags: code.CompatibilityFlags, Size: len(raw), ObjectKey: "v1/org-a/archive.tar.gz", StorageLocation: "test", Bucket: "artifacts", Created: now()}
	op := WorkerOperation{ID: "op-cas", Kind: "publish", Service: "svc-a", Organization: "org-a", Environment: "production", State: "validating", Release: release.ID, ExpectedRevision: 99, Created: now()}
	state := WorkerState{
		Releases: map[string]WorkerRelease{release.ID: release}, Artifacts: map[string]WorkerArtifact{artifact.ID: artifact},
		Operations: map[string]WorkerOperation{op.ID: op}, OperationPrivate: map[string]WorkerOperationPrivate{op.ID: {Actor: WorkerActor{User: "admin", AuthVersion: 1}}},
	}
	if err := controller.processOperation(context.Background(), op, state, true); err != nil {
		t.Fatal(err)
	}
	latest, _, err := store.ConfigState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if latest != 0 {
		t.Fatalf("commit CAS conflict still published edge config revision %d", latest)
	}
}

func TestEnsureRoutesPrunesGatewayWithoutHosts(t *testing.T) {
	fake := &fakeKube{routes: map[string]map[string]any{}, gateways: map[string]bool{}}
	srv := fake.server(t)
	defer srv.Close()
	c := catalogConfig()
	if err := c.validate(); err != nil {
		t.Fatal(err)
	}
	k := &workerKubernetes{base: srv.URL, namespace: "workers-mvp", client: srv.Client()}
	if _, err := k.ensureRoutes(context.Background(), c, "workers-abc", c.RuntimePools[0].RuntimeService, map[string][]string{
		"internal": {"w-1-production.workers.test"},
		"external": {"w-1-production.workers-out.example.test"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := k.ensureRoutes(context.Background(), c, "workers-abc", c.RuntimePools[0].RuntimeService, map[string][]string{
		"internal": {"w-1-production.workers.test"},
	}); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, name := range fake.deleted {
		if name == "workers-abc-external" {
			found = true
		}
	}
	if !found {
		t.Fatalf("external route was not pruned: %v", fake.deleted)
	}
}

func TestStaticRoutesFollowDomainGatewayBinding(t *testing.T) {
	fake := &fakeKube{routes: map[string]map[string]any{}, gateways: map[string]bool{}}
	srv := fake.server(t)
	defer srv.Close()
	c := catalogConfig()
	if err := c.validate(); err != nil {
		t.Fatal(err)
	}
	k := &workerKubernetes{base: srv.URL, namespace: "workers-mvp", client: srv.Client()}
	for _, d := range c.Domains {
		if err := k.ensureStaticRoute(context.Background(), c, d.ID, "assets"); err != nil {
			t.Fatal(err)
		}
	}
	foundInternal, foundExternal := false, false
	for name, spec := range fake.routes {
		host := spec["hostnames"].([]any)[0].(string)
		p := spec["parentRefs"].([]any)[0].(map[string]any)
		switch host {
		case "static.workers.test":
			foundInternal = true
			if p["name"] != "workers-mvp" {
				t.Fatalf("internal static route on %v (%s)", p, name)
			}
		case "static.workers-out.example.test":
			foundExternal = true
			if p["name"] != "workers-public" {
				t.Fatalf("external static route on %v (%s)", p, name)
			}
		}
	}
	if !foundInternal || !foundExternal {
		t.Fatalf("static routes missing: %v", fake.routes)
	}
}

func TestDomainCatalogAPIExposesLabels(t *testing.T) {
	s, _, _ := workerTestServer(t)
	s.Config.Workers = catalogConfig()
	if err := s.Config.Workers.validate(); err != nil {
		t.Fatal(err)
	}
	s.routes()
	r := request(s, "GET", "/api/v1/orgs/org-a/workers/domains", "krun_cli_viewer", "")
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	var list struct {
		Items []WorkerDomainView `json:"items"`
	}
	_ = json.Unmarshal(r.Body.Bytes(), &list)
	if len(list.Items) != 2 {
		t.Fatalf("domains: %+v", list.Items)
	}
	for _, d := range list.Items {
		if d.Label == "" {
			t.Fatalf("domain entry incomplete: %+v", d)
		}
	}
	if strings.Contains(r.Body.String(), `"access"`) || strings.Contains(r.Body.String(), `"gateway"`) || strings.Contains(r.Body.String(), `"static_domain"`) {
		t.Fatalf("infrastructure classification leaked: %s", r.Body.String())
	}
}
