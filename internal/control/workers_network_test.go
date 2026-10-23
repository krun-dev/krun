package control

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
)

// catalogConfig models the current deployment: two Gateway instances and two
// domains whose labels are purely descriptive.
func catalogConfig() *WorkersConfig {
	return &WorkersConfig{
		Namespace:       "workers-mvp",
		BuilderMode:     "local",
		BuilderScript:   "workers/builder/build-worker.mjs",
		RuntimeService:  "runtime-default",
		RuntimeToken:    strings.Repeat("r", 24),
		ManagementToken: strings.Repeat("m", 24),
		S3Endpoint:      "http://127.0.0.1:9000",
		S3Bucket:        "krun-artifacts",
		S3AccessKey:     "access",
		S3SecretKey:     "secret",
		Gateways: []WorkerGateway{
			{ID: "internal", Name: "workers-mvp", Namespace: "kgateway-system", Section: "private", Base: "http://127.0.0.1:38080"},
			{ID: "external", Name: "workers-public", Namespace: "kgateway-system", Section: "public", Base: "http://127.0.0.1:38081"},
		},
		Domains: []WorkerDomain{
			{ID: "internal", Domain: "workers.test", Label: "内网", Gateway: "internal", Default: true},
			{ID: "external", Domain: "workers-out.example.test", Label: "公网", Gateway: "external"},
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
	r := request(s, "PUT", base+"/domains", "krun_cli_admin", `{"domains":["internal","external"]}`)
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
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
	r = request(s, "PUT", base+"/domains", "krun_cli_admin", `{"domains":["internal"]}`)
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	_ = store.Update(context.Background(), func(st *State) error {
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
	}
	_ = json.Unmarshal(r.Body.Bytes(), &out)
	if len(out.Selected) != 1 || out.Selected[0] != "internal" {
		t.Fatalf("selected: %v", out.Selected)
	}
	if len(out.Catalog) != 2 {
		t.Fatalf("catalog: %+v", out.Catalog)
	}
}

func TestWorkerConfigProjectionCoversEveryDomain(t *testing.T) {
	st, _ := workerConfigFixture()
	ws := st.Workers.Services["svc-a"]
	prod := ws.Environments["production"]
	prod.Hosts = []string{"w-prod.workers.test", "w-prod.workers-out.example.test"}
	ws.Environments["production"] = prod
	st.Workers.Services["svc-a"] = ws
	p := workerConfigFromState(st)
	hosts := make([]string, 0, len(p.Routes))
	for _, route := range p.Routes {
		hosts = append(hosts, route.Host)
	}
	sort.Strings(hosts)
	want := []string{"w-empty.workers.test", "w-prev.workers.test", "w-prod.workers-out.example.test", "w-prod.workers.test"}
	if strings.Join(hosts, ",") != strings.Join(want, ",") {
		t.Fatalf("projection hosts: %v", hosts)
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
	hosts := map[string][]string{
		"internal": {"w-1-production.workers.test"},
		"external": {"w-1-production.workers-out.example.test"},
	}
	if ready, err := k.ensureRoutes(context.Background(), c, "workers-abc", hosts); err != nil || ready {
		t.Fatalf("creation pass: %v ready=%v", err, ready)
	}
	if ready, err := k.ensureRoutes(context.Background(), c, "workers-abc", hosts); err != nil || !ready {
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
	for _, raw := range external["hostnames"].([]any) {
		if strings.HasSuffix(raw.(string), "."+c.Domains[0].Domain) {
			t.Fatalf("internal host leaked onto the external route: %v", external["hostnames"])
		}
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
	if _, err := k.ensureRoutes(context.Background(), c, "workers-abc", map[string][]string{
		"internal": {"w-1-production.workers.test"},
		"external": {"w-1-production.workers-out.example.test"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := k.ensureRoutes(context.Background(), c, "workers-abc", map[string][]string{
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
		if d.Label == "" || d.Gateway == "" {
			t.Fatalf("domain entry incomplete: %+v", d)
		}
	}
}
