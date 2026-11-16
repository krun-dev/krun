package control

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	kruncli "github.com/krun-dev/krun/internal/cli"
	appconfig "github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/domain"
	workerapp "github.com/krun-dev/krun/internal/workers"
)

func addTypedHostnameSuffixes(cfg *appconfig.WorkersConfig) (application, custom appconfig.WorkerDomain) {
	defaultSuffix := cfg.Domains[0]
	application = appconfig.WorkerDomain{
		ID: "apps", Domain: "apps.test", Types: []appconfig.WorkerDomainType{appconfig.WorkerDomainApplication},
		Access: defaultSuffix.Access, Gateway: defaultSuffix.Gateway, Labels: defaultSuffix.Labels,
	}
	custom = appconfig.WorkerDomain{
		ID: "custom", Domain: "custom.test", Types: []appconfig.WorkerDomainType{appconfig.WorkerDomainCustom},
		Access: defaultSuffix.Access, Gateway: defaultSuffix.Gateway, Labels: defaultSuffix.Labels,
	}
	cfg.Domains = append(cfg.Domains, application, custom)
	return application, custom
}

func TestManagedHostnameOwnershipAndRuntimeProjection(t *testing.T) {
	s, store, worker := workerTestServer(t)
	ctx := t.Context()
	cfg := s.Config.Workers
	suffix, _ := addTypedHostnameSuffixes(cfg)
	cfg.Domains[1].ReservedHosts = []string{"reserved." + suffix.Domain}
	host := "shop." + suffix.Domain
	secondWorker := "svc-api"
	err := store.Update(ctx, PartitionAll, func(st *State) error {
		st.Resources[secondWorker] = Resource{ID: secondWorker, Organization: "org-a", Kind: "worker", Locator: "orders", Name: "Orders", Revision: 1}
		s.worker(st, Target{ID: secondWorker, Organization: "org-a"})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	base := "/api/v1/orgs/org-a/worker-hostnames"
	admin := "krun_cli_admin"
	payload := fmt.Sprintf(`{"suffix_id":%q,"label":"Shop","routes":[
		{"match":"prefix","path":"/","worker_id":"svc-a","enabled":true},
		{"match":"prefix","path":"/api/","worker_id":"orders","enabled":true},
		{"match":"exact","path":"/api","worker_id":"svc-a","enabled":true}
	]}`, suffix.ID)
	do := func(method, path, token, body string, status int, code string) string {
		t.Helper()
		res := request(s, method, path, token, body)
		if res.Code != status || (code != "" && !strings.Contains(res.Body.String(), code)) {
			t.Fatalf("%s %s: expected %d %s, got %d %s", method, path, status, code, res.Code, res.Body.String())
		}
		return res.Body.String()
	}

	do(http.MethodPost, base, "krun_cli_viewer", payload, 403, "PERMISSION_DENIED")
	do(http.MethodPost, base, admin, strings.Replace(payload, "Shop", "reserved", 1), 409, "HOSTNAME_UNAVAILABLE")
	do(http.MethodPost, base, admin, strings.Replace(payload, "Shop", "a.b", 1), 422, "HOSTNAME_INVALID")
	do(http.MethodPost, base, admin, strings.Replace(payload, "Shop", "K", 1), 422, "HOSTNAME_INVALID")
	do(http.MethodPost, base, admin, strings.Replace(payload, "orders", "svc-b", 1), 422, "HOSTNAME_TARGET_INVALID")
	do(http.MethodPost, base, admin, strings.Replace(payload, `"match":"exact"`, `"match":"prefix"`, 1), 409, "HOSTNAME_ROUTE_CONFLICT")
	do(http.MethodPost, base, admin, payload, 201, host)
	do(http.MethodPost, "/api/v1/orgs/org-b/worker-hostnames", admin, fmt.Sprintf(`{"suffix_id":%q,"label":"SHOP"}`, suffix.ID), 409, "HOSTNAME_UNAVAILABLE")
	bindings := do(http.MethodGet, worker+"/hostnames", "krun_cli_viewer", "", 200, host)
	if strings.Contains(bindings, secondWorker) {
		t.Fatalf("Worker-level bindings disclosed another target: %s", bindings)
	}
	var recorded int
	if err := postgresTestPool(store).QueryRow(ctx, `SELECT count(*) FROM krun_audits
		WHERE action='workers.hostnames.claim' AND resource=$1 AND organization_id='org-a'`, host).Scan(&recorded); err != nil || recorded != 1 {
		t.Fatalf("claim audit is missing from the organization: count=%d err=%v", recorded, err)
	}
	do(http.MethodPut, base+"/"+host, admin, `{"expected_revision":2,"routes":[]}`, 409, "REVISION_CONFLICT")

	controller := &workerapp.Controller{Config: cfg, Service: s.workerApplication(nil)}
	publish := func() int64 {
		t.Helper()
		revision, err := controller.PublishConfigDelta(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return revision
	}
	first := publish()
	pool, err := cfg.DomainRuntimePool(suffix.ID)
	if err != nil {
		t.Fatal(err)
	}
	routeSet := func() []domain.EdgeConfigRoute {
		t.Helper()
		projection, _, err := store.RuntimeConfigRoute(ctx, host, pool.ID)
		if err != nil {
			t.Fatal(err)
		}
		return projection.Routes
	}
	initial := routeSet()
	wire, err := s.runtimeApplication().Route(ctx, host, pool.ID)
	if err != nil || len(wire.Routes) != 0 || len(wire.ManagedRoutes) != len(initial) {
		t.Fatalf("managed routes must be invisible to legacy runtimes: %+v %v", wire, err)
	}
	if len(initial) != 3 || initial[0].Match != "exact" || initial[2].Path != "/api" || initial[2].Worker != secondWorker {
		t.Fatalf("incorrect shared route set: %+v", initial)
	}

	// Modify one target's environment without touching the other Worker. The
	// config delta must still retain the unrelated routes on the shared host.
	if _, err := postgresTestPool(store).Exec(ctx, `UPDATE krun_worker_environments
		SET enabled=false WHERE worker_id=$1 AND env_name='production'`, secondWorker); err != nil {
		t.Fatal(err)
	}
	publish()
	after := routeSet()
	if len(after) != 3 || after[2].Enabled || !after[0].Enabled || !after[1].Enabled {
		t.Fatalf("incorrect rules after target update: %+v", after)
	}
	deltas, _, _, err := store.ConfigRevisionsSince(ctx, first, 10)
	if err != nil || len(deltas) != 1 {
		t.Fatalf("delta: %+v %v", deltas, err)
	}
	var ops []domain.EdgeConfigOp
	if err := json.Unmarshal(deltas[0].Ops, &ops); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, op := range ops {
		if op.Host == host && op.RouteKey == "prefix:/api" && op.Type == "route_upsert" {
			found = true
		}
	}
	if !found {
		t.Fatalf("managed route key missing from feed: %+v", ops)
	}

	do(http.MethodDelete, base+"/"+host+"?revision=1", admin, "", 204, "")
	publish()
	_, _, err = store.RuntimeConfigRoute(ctx, host, pool.ID)
	var apiErr *domain.APIError
	if !errors.As(err, &apiErr) || apiErr.Kind != domain.ErrorNotFound {
		t.Fatalf("retired Host still in projection: %v", err)
	}
	do(http.MethodPost, base, admin, payload, 409, "HOSTNAME_UNAVAILABLE")
}

func TestCLIManagedHostnameWorkflow(t *testing.T) {
	s, _, _ := workerTestServer(t)
	suffix, customSuffix := addTypedHostnameSuffixes(s.Config.Workers)
	server := httptest.NewServer(s)
	defer server.Close()
	t.Setenv("KRUN_ENDPOINT", server.URL)
	t.Setenv("KRUN_TOKEN", "krun_cli_admin")
	dir := t.TempDir()
	routes := filepath.Join(dir, "routes.json")
	if err := os.WriteFile(routes, []byte(`[{"match":"prefix","path":"/api","worker_id":"svc-a","enabled":true}]`), 0600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) string {
		t.Helper()
		var out, diagnostics bytes.Buffer
		flags := append([]string{"--no-update-check", "--profile", filepath.Join(dir, "profile.yaml")}, args...)
		flags = append(flags, "--org", "org-a")
		if err := kruncli.Run(t.Context(), flags, strings.NewReader(""), &out, &diagnostics); err != nil {
			t.Fatalf("krun %v: %v (%s)", args, err, diagnostics.String())
		}
		return out.String()
	}

	var claimed domain.ManagedHostname
	raw := run("domains", "claim", "--suffix", suffix.ID, "--label", "cli", "--routes", routes)
	if err := json.Unmarshal([]byte(raw), &claimed); err != nil || claimed.Host != "cli."+suffix.Domain || claimed.Revision != 1 {
		t.Fatalf("CLI claim result %q: %v", raw, err)
	}
	if out := run("domains", "list", "--worker", "svc-a"); !strings.Contains(out, claimed.Host) || !strings.Contains(out, "/api") {
		t.Fatalf("CLI Worker bindings did not include the new route: %s", out)
	}
	raw = run("domains", "set", "--host", claimed.Host, "--revision", "1", "--routes", routes)
	var updated domain.ManagedHostname
	if err := json.Unmarshal([]byte(raw), &updated); err != nil || updated.Revision != 2 {
		t.Fatalf("CLI update result %q: %v", raw, err)
	}
	run("domains", "retire", "--host", claimed.Host, "--revision", "2")
	if out := run("domains", "list"); strings.Contains(out, claimed.Host) {
		t.Fatalf("CLI still lists a retired hostname: %s", out)
	}
	attached := run("workers", "custom-domains", "attach", "--worker", "svc-a", "--suffix", customSuffix.ID, "--label", "cli")
	var custom domain.ManagedHostname
	if err := json.Unmarshal([]byte(attached), &custom); err != nil || custom.Type != string(appconfig.WorkerDomainCustom) ||
		custom.Host != "cli."+customSuffix.Domain || len(custom.Routes) != 1 || !custom.Routes[0].CustomDomain || !custom.Routes[0].PreviewsEnabled {
		t.Fatalf("CLI custom domain result %q: %v", attached, err)
	}
	if out := run("domains", "list"); strings.Contains(out, custom.Host) {
		t.Fatalf("custom domain leaked into application catalog: %s", out)
	}
	run("workers", "custom-domains", "detach", "--worker", "svc-a", "--host", custom.Host, "--revision", "1")
	if out := run("workers", "custom-domains", "list", "--worker", "svc-a"); strings.Contains(out, custom.Host) {
		t.Fatalf("detached custom domain still appears on Worker: %s", out)
	}

}

func TestCloudflareHostnameAPIsShareOrganizationRoutes(t *testing.T) {
	s, store, _ := workerTestServer(t)
	cfg := s.Config.Workers
	suffix, customSuffix := addTypedHostnameSuffixes(cfg)
	host := "shop." + suffix.Domain
	cfg.ManagedOrigins = []appconfig.ManagedOrigin{
		{Host: "shop-static.origin.example.com", Kind: "static", Service: "storefront", Port: 80, Organizations: []string{"org-a"}, Suffixes: []string{suffix.ID}},
		{Host: "legacy.origin.example.com", Kind: "http", Service: "legacy", Port: 8080, Organizations: []string{"org-a"}, Suffixes: []string{suffix.ID}},
	}
	if _, err := postgresTestPool(store).Exec(t.Context(), `UPDATE krun_resources SET locator='storefront' WHERE resource_id='svc-a'`); err != nil {
		t.Fatal(err)
	}

	cloud := cfPrefix
	native := "/api/v1/orgs/org-a/domains"
	account := cloud + "/accounts/org-a"
	script := account + "/workers/scripts/storefront"
	admin := "krun_cli_admin"
	do := func(method, path, token, body string, status int) []byte {
		t.Helper()
		res := request(s, method, path, token, body)
		if res.Code != status {
			t.Fatalf("%s %s: want %d, got %d: %s", method, path, status, res.Code, res.Body.String())
		}
		return res.Body.Bytes()
	}
	var zones struct {
		Result []struct{ ID, Name string }
	}
	raw := do(http.MethodGet, cloud+"/zones?name="+suffix.Domain+"&account.id=org-a", admin, "", 200)
	if err := json.Unmarshal(raw, &zones); err != nil || len(zones.Result) != 1 || zones.Result[0].Name != suffix.Domain {
		t.Fatalf("zone discovery: %s (%v)", raw, err)
	}
	zone := cloud + "/zones/" + zones.Result[0].ID
	pattern := host + "/api/*"
	route := fmt.Sprintf(`{"pattern":%q,"script":"storefront"}`, pattern)
	raw = do(http.MethodPost, zone+"/workers/routes", admin, route, 200)
	var created struct{ Result struct{ ID, Pattern string } }
	if err := json.Unmarshal(raw, &created); err != nil || created.Result.ID == "" || created.Result.Pattern != pattern {
		t.Fatalf("route result: %s (%v)", raw, err)
	}
	do(http.MethodGet, zone+"/workers/routes/"+created.Result.ID, admin, "", 200)
	do(http.MethodPut, script+"/routes", admin, fmt.Sprintf(`[{"pattern":%q,"zone_name":%q}]`, pattern, suffix.Domain), 200)

	origins := zone + "/rulesets/phases/http_request_origin/entrypoint"
	expression := "http.host eq " + fmt.Sprintf("%q", host)
	payload := fmt.Sprintf(`{"rules":[
		{"action":"route","expression":%q,"action_parameters":{"origin":{"host":"shop-static.origin.example.com"}}},
		{"action":"route","expression":%q,"enabled":false,"action_parameters":{"origin":{"host":"legacy.origin.example.com","port":8080}}}
	]}`, expression, expression+` and (http.request.uri.path eq "/legacy" or starts_with(http.request.uri.path, "/legacy/"))`)
	do(http.MethodPut, origins, admin, payload, 200)
	item, err := store.ManagedHostname(t.Context(), "org-a", host)
	if err != nil || !item.GatewayManaged || len(item.Routes) != 3 {
		t.Fatalf("mixed routes: %+v (%v)", item, err)
	}
	raw = do(http.MethodGet, native, admin, "", 200)
	for _, expected := range []string{host, "shop-static.origin.example.com", "/api", "/legacy"} {
		if !strings.Contains(string(raw), expected) {
			t.Fatalf("organization domain list omitted %q: %s", expected, raw)
		}
	}

	custom := "worker." + customSuffix.Domain
	do(http.MethodPut, account+"/workers/domains", admin, fmt.Sprintf(`{"hostname":%q,"service":"storefront"}`, host), 422)
	do(http.MethodPut, script+"/routes", admin, fmt.Sprintf(`[{"pattern":%q}]`, custom+"/api/*"), 422)
	do(http.MethodPut, script+"/routes", admin, fmt.Sprintf(`[{"pattern":%q}]`, "test."+cfg.Domains[0].Domain+"/*"), 422)
	do(http.MethodPost, script+"/domains/changeset?replace_state=true", admin, fmt.Sprintf(`[{"hostname":%q}]`, custom), 200)
	do(http.MethodPut, script+"/domains/records", admin, fmt.Sprintf(`{"override_scope":true,"origins":[{"hostname":%q}]}`, custom), 200)
	raw = do(http.MethodGet, account+"/workers/domains?hostname="+custom, admin, "", 200)
	var records struct {
		Result []struct{ ID, Hostname string }
	}
	if err = json.Unmarshal(raw, &records); err != nil || len(records.Result) != 1 || records.Result[0].Hostname != custom {
		t.Fatalf("custom domain result: %s (%v)", raw, err)
	}
	do(http.MethodDelete, account+"/workers/domains/"+records.Result[0].ID, admin, "", 200)
	do(http.MethodDelete, zone+"/workers/routes/"+created.Result.ID, admin, "", 200)
	do(http.MethodPut, origins, admin, strings.Replace(payload, "legacy.origin.example.com", "private.other.example.com", 1), 422)
	item, err = store.ManagedHostname(t.Context(), "org-a", host)
	if err != nil || len(item.Routes) != 2 {
		t.Fatalf("a rejected ruleset update changed the remaining routes: %+v (%v)", item, err)
	}
}

// Opt in with the Wrangler version installed by workers/builder. This talks to
// a real HTTP server using the same unmodified CLI that users run in CI.
func TestOfficialWranglerHostnameTriggers(t *testing.T) {
	binary := os.Getenv("KRUN_TEST_WRANGLER")
	if binary == "" {
		t.Skip("set KRUN_TEST_WRANGLER to the absolute path of the Wrangler CLI")
	}
	s, store, _ := workerTestServer(t)
	network := s.Config.Workers
	network.Domains[0].Types = append(network.Domains[0].Types, appconfig.WorkerDomainCustom, appconfig.WorkerDomainApplication)
	suffix := network.Domains[0]
	host := "wrangler-app." + suffix.Domain
	custom := "wrangler-custom." + suffix.Domain
	previewOnly := "wrangler-preview." + suffix.Domain
	if _, err := postgresTestPool(store).Exec(t.Context(), `
		UPDATE krun_resources SET locator='storefront' WHERE resource_id='svc-a';
		UPDATE krun_organizations SET short_name='store-org' WHERE org_id='org-a';
	`); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(s)
	defer server.Close()
	dir := t.TempDir()
	cfg := fmt.Sprintf(`{
		"name":"storefront", "account_id":"store-org", "main":"index.js", "compatibility_date":"2026-09-28", "preview_urls":false,
		"routes":[{"pattern":%q,"zone_name":%q},{"pattern":%q,"custom_domain":true},
			{"pattern":%q,"custom_domain":true,"previews_enabled":true,"enabled":false}]
	}`, host+"/api/*", suffix.Domain, custom, previewOnly)
	if err := os.WriteFile(filepath.Join(dir, "wrangler.jsonc"), []byte(cfg), 0600); err != nil {
		t.Fatal(err)
	}
	source := "export default {\n  fetch() {\n    return new Response('ok');\n  },\n};\n"
	if err := os.WriteFile(filepath.Join(dir, "index.js"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}

	for range 2 {
		cmd := exec.CommandContext(t.Context(), binary, "triggers", "deploy", "--config", "wrangler.jsonc")
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"CLOUDFLARE_API_BASE_URL="+server.URL+cfPrefix,
			"CLOUDFLARE_API_TOKEN=krun_cli_admin",
			"WRANGLER_SEND_METRICS=false",
			"WRANGLER_LOG_PATH="+filepath.Join(dir, "wrangler.log"),
			"CI=true",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("Wrangler triggers deploy: %v\n%s", err, out)
		}
	}
	item, err := store.ManagedHostname(t.Context(), "org-a", host)
	if err != nil || item.Type != string(appconfig.WorkerDomainApplication) || item.SuffixID != suffix.ID || len(item.Routes) != 1 || item.Routes[0].Path != "/api" {
		t.Fatalf("Wrangler did not publish the application route: %+v (%v)", item, err)
	}
	item, err = store.ManagedHostname(t.Context(), "org-a", custom)
	if err != nil || item.Type != string(appconfig.WorkerDomainCustom) || item.SuffixID != suffix.ID || len(item.Routes) != 1 || !item.Routes[0].CustomDomain || !item.Routes[0].PreviewsEnabled {
		t.Fatalf("Wrangler did not publish the standalone Custom Domain: %+v (%v)", item, err)
	}
	for _, tc := range []struct{ method, path, body string }{
		{"PUT", cfPrefix + "/accounts/org-a/workers/scripts/storefront/routes", fmt.Sprintf(`[{"pattern":%q,"zone_name":%q}]`, custom+"/api/*", suffix.Domain)},
		{"PUT", cfPrefix + "/accounts/org-a/workers/domains", fmt.Sprintf(`{"hostname":%q,"service":"storefront"}`, host)},
		{"POST", "/api/v1/orgs/org-a/worker-hostnames", fmt.Sprintf(`{"suffix_id":%q,"label":"wrangler-custom"}`, suffix.ID)},
		{"POST", "/api/v1/orgs/org-a/worker-hostnames", fmt.Sprintf(`{"suffix_id":%q,"label":"w-generated"}`, suffix.ID)},
	} {
		resp := request(s, tc.method, tc.path, "krun_cli_admin", tc.body)
		if resp.Code != 409 {
			t.Fatalf("%s %s: expected hostname conflict, got %d %s", tc.method, tc.path, resp.Code, resp.Body.String())
		}
	}
	resp := request(s, "GET", cfPrefix+"/accounts/org-a/workers/scripts/storefront/subdomain", "krun_cli_admin", "")
	if resp.Code != 200 || !strings.Contains(resp.Body.String(), `"enabled":false,"previews_enabled":false`) {
		t.Fatalf("Wrangler's defaults did not persist: %d %s", resp.Code, resp.Body.String())
	}
	previewAPI := cfPrefix + "/accounts/org-a/workers/workers/storefront/previews"
	resp = request(s, "POST", previewAPI, "krun_cli_admin", `{"name":"pr-123"}`)
	var result struct {
		Result struct{ URLs []string }
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &result); resp.Code != 200 || err != nil ||
		len(result.Result.URLs) != 2 || !strings.HasSuffix(result.Result.URLs[0], "pr-123."+custom) ||
		!strings.HasSuffix(result.Result.URLs[1], "pr-123."+previewOnly) {
		t.Fatalf("preview-only API response: %d %s (%v)", resp.Code, resp.Body.String(), err)
	}
	state, err := store.EdgeConfigState(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	projection := workerapp.EdgeConfigFromState(state, s.Config.Workers)
	want := map[string]string{
		host: "production", custom: "production",
		"pr-123." + custom: "pr-123", "pr-123." + previewOnly: "pr-123",
		"fix-demo." + custom: "fix-demo", "fix-demo." + previewOnly: "fix-demo",
	}
	for _, route := range projection.Routes {
		if route.Worker != "svc-a" {
			continue
		}
		if want[route.Host] != route.Environment || !route.Enabled {
			t.Fatalf("unexpected route: %+v", route)
		}
		delete(want, route.Host)
	}
	if len(want) != 0 {
		t.Fatalf("missing routes: %v", want)
	}
}
