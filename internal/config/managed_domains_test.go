package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagedSuffixLoadsThroughMainConfig(t *testing.T) {
	setExampleCredentials(t)
	t.Setenv("KRUN_DATABASE_URL", "postgres://fixture/krun_test")
	raw, err := os.ReadFile(filepath.Join("..", "..", "config", "example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	const typedSuffixes = `
    - id: custom
      type: worker_custom
      domain: custom.example.com
      access: public
      labels: {exposure: external}
      gateway: external
    - id: apps
      type: application
      domain: apps.example.com
      access: public
      labels: {exposure: external}
      gateway: external
      reserved_hosts: []
`
	base := strings.Replace(string(raw), "      reserved_hosts: []\n", typedSuffixes, 1)
	file := filepath.Join(t.TempDir(), "main.yaml")
	write := func(text string) {
		t.Helper()
		if err := os.WriteFile(file, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}

	write(strings.Replace(base, "reserved_hosts: []", "reserved_hosts: [reserved.apps.example.com]", 1))
	cfg, err := LoadConfig(file)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Workers.Domains[1].Type != WorkerDomainCustom || cfg.Workers.Domains[2].Type != WorkerDomainApplication || !cfg.Workers.ManagedHostReserved("reserved.apps.example.com") {
		t.Fatal("managed suffix or reserved hostname was not applied")
	}

	write(strings.Replace(base, "reserved_hosts: []", "reserved_hosts: [other.example.com]", 1))
	if _, err = LoadConfig(file); err == nil || !strings.Contains(err.Error(), "invalid reserved host") {
		t.Fatalf("reserved host outside its suffix: %v", err)
	}

	write(strings.Replace(base, "      type: worker_default\n", "", 1))
	if _, err = LoadConfig(file); err == nil || !strings.Contains(err.Error(), "requires type") {
		t.Fatalf("untyped suffix accepted: %v", err)
	}
	write(strings.Replace(base, "domain: apps.example.com", "domain: apps.workers.example.com", 1))
	if _, err = LoadConfig(file); err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Fatalf("overlapping suffixes accepted: %v", err)
	}

	const originBlock = `
  managed_origins:
    - host: storefront.origin.example.com
      kind: static
      service: storefront
      namespace: web
      port: 80
      host_header: site.example.com
      organizations: [org-a]
      suffixes: [apps]
`
	withOrigin := strings.Replace(base, "  # managed_origins:\n", originBlock, 1)
	write(withOrigin)
	cfg, err = LoadConfig(file)
	if err != nil {
		t.Fatal(err)
	}
	origin, ok := cfg.Workers.ManagedOriginFor("org-a", "apps", "storefront.origin.example.com")
	if !ok || origin.Service != "storefront" || origin.Namespace != "web" || origin.HostHeader != "site.example.com" {
		t.Fatalf("registered origin did not load: %+v", origin)
	}
	if _, ok = cfg.Workers.ManagedOriginFor("org-b", "apps", origin.Host); ok {
		t.Fatal("origin unexpectedly available outside the configured organizations")
	}
	write(strings.Replace(withOrigin, "suffixes: [apps]", "suffixes: [unknown]", 1))
	if _, err = LoadConfig(file); err == nil || !strings.Contains(err.Error(), "requires an application suffix") {
		t.Fatalf("origin with unknown suffix: %v", err)
	}
}
