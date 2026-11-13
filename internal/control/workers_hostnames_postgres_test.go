package control

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	kruncli "github.com/krun-dev/krun/internal/cli"
	"github.com/krun-dev/krun/internal/domain"
	workerapp "github.com/krun-dev/krun/internal/workers"
)

func TestManagedHostnameOwnershipAndRuntimeProjection(t *testing.T) {
	s, store, worker := workerTestServer(t)
	ctx := t.Context()
	cfg := s.Config.Workers
	cfg.Domains[0].ManagedSubdomains = true
	cfg.Domains[0].ReservedHosts = []string{"reserved." + cfg.Domains[0].Domain}
	suffix := cfg.Domains[0]
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
	do(http.MethodPost, base, admin, strings.Replace(payload, "Shop", "w-owned", 1), 409, "HOSTNAME_UNAVAILABLE")
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
	suffix := &s.Config.Workers.Domains[0]
	suffix.ManagedSubdomains = true
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
		flags := append([]string{"--no-update-check", "--profile", filepath.Join(dir, "profile.yaml"), "workers", "hostnames"}, args...)
		flags = append(flags, "--org", "org-a")
		if err := kruncli.Run(t.Context(), flags, strings.NewReader(""), &out, &diagnostics); err != nil {
			t.Fatalf("krun %v: %v (%s)", args, err, diagnostics.String())
		}
		return out.String()
	}

	var claimed domain.ManagedHostname
	raw := run("claim", "--suffix", suffix.ID, "--label", "cli", "--routes", routes)
	if err := json.Unmarshal([]byte(raw), &claimed); err != nil || claimed.Host != "cli."+suffix.Domain || claimed.Revision != 1 {
		t.Fatalf("CLI claim result %q: %v", raw, err)
	}
	if out := run("list", "--worker", "svc-a"); !strings.Contains(out, claimed.Host) || !strings.Contains(out, "/api") {
		t.Fatalf("CLI Worker bindings did not include the new route: %s", out)
	}
	raw = run("set", "--host", claimed.Host, "--revision", "1", "--routes", routes)
	var updated domain.ManagedHostname
	if err := json.Unmarshal([]byte(raw), &updated); err != nil || updated.Revision != 2 {
		t.Fatalf("CLI update result %q: %v", raw, err)
	}
	run("retire", "--host", claimed.Host, "--revision", "2")
	if out := run("list"); strings.Contains(out, claimed.Host) {
		t.Fatalf("CLI still lists a retired hostname: %s", out)
	}
}
