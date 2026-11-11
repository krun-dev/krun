package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProtectedEgressLoadsThroughMainConfig(t *testing.T) {
	setExampleCredentials(t)
	t.Setenv("KRUN_DATABASE_URL", "postgres://fixture/krun_test")
	c, err := LoadConfig(filepath.Join("..", "..", "config", "example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		input string
		want  string
	}{
		{"192.0.2.53/24", "192.0.2.0/24"},
		{"2001:db8::/48", "2001:db8::/48"},
		{"example.com", ""},
		{"::ffff:192.0.2.1/128", ""},
	} {
		t.Run(test.input, func(t *testing.T) {
			c.Workers.ProtectedEgressCIDRs = []string{test.input}
			raw, err := json.Marshal(c)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "main.yaml")
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			loaded, err := LoadConfig(path)
			if test.want == "" {
				if err == nil {
					t.Fatal("invalid protected egress CIDR was accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := loaded.Workers.ProtectedEgressCIDRs; len(got) != 1 || got[0] != test.want {
				t.Fatalf("protected egress CIDRs = %v, want %s", got, test.want)
			}
		})
	}
}

func TestWorkerEgressDoesNotInheritIngress(t *testing.T) {
	pool := WorkerRuntimePool{Access: []string{"public", "private"}}
	if err := NormalizeWorkerEgress(&pool); err != nil {
		t.Fatal(err)
	}
	if pool.PublicEgress != "deny" || pool.PrivateEgress != "deny" {
		t.Fatalf("omitted egress fields must default to deny: %+v", pool)
	}
}

func TestHyperdriveLoadsThroughMainConfig(t *testing.T) {
	setExampleCredentials(t)
	t.Setenv("KRUN_DATABASE_URL", "postgres://fixture/krun_test")
	cfg, err := LoadConfig(filepath.Join("..", "..", "config", "example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Development = true
	cfg.PublicURL = "http://127.0.0.1:8080"
	cfg.Listen = "127.0.0.1:8080"
	cfg.InternalListen = "127.0.0.1:8081"
	cfg.Workers.Hyperdrive = &WorkerHyperdriveConfig{
		Listen: "127.0.0.2:6432", Address: "proxy.example.test:6432",
		TLSCertFile: "/fixture/proxy.crt", TLSKeyFile: "/fixture/proxy.key",
	}
	path := filepath.Join(t.TempDir(), "main.yaml")
	load := func() (Config, error) {
		raw, err := json.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		return LoadConfig(path)
	}
	got, err := load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Workers.Hyperdrive.CIDR() != "127.0.0.2/32" {
		t.Fatalf("proxy address was not loaded: %+v", got.Workers.Hyperdrive)
	}
	cfg.InternalListen = "127.0.0.2:8081"
	if _, err := load(); err == nil {
		t.Fatal("HTTP and Hyperdrive listeners may not share the dedicated address")
	}
}

func TestHyperdriveRequiredWithWorkers(t *testing.T) {
	setExampleCredentials(t)
	t.Setenv("KRUN_DATABASE_URL", "postgres://fixture/krun_test")
	c, err := LoadConfig(filepath.Join("..", "..", "config", "example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	c.Workers.Hyperdrive = nil
	path := filepath.Join(t.TempDir(), "main.yaml")
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "workers.hyperdrive is required") {
		t.Fatalf("workers without hyperdrive accepted: %v", err)
	}
	// The Kubernetes render pipeline rejects the section explicitly instead of
	// half-configuring the proxy.
	c.Workers.Hyperdrive = &WorkerHyperdriveConfig{Listen: "10.10.0.10:6432", Address: "hyperdrive.example.test:6432", TLSCertFile: "/fixture/proxy.crt", TLSKeyFile: "/fixture/proxy.key"}
	c.Deployment = &DeploymentConfig{
		ControlImage: "registry.example.com/krun/control:v1", RuntimeImage: "registry.example.com/krun/runtime:v1", DNSResolver: "192.0.2.53",
		Network: DeploymentNetwork{
			Control: []DeploymentEgress{{PublicHTTPS: true}},
			Runtime: []DeploymentEgress{{CIDRs: []string{"192.0.2.0/24"}, Ports: []int{443, 6432}}},
			Builder: []DeploymentEgress{{PublicHTTPS: true}},
		},
	}
	raw, err = json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "not supported by the Kubernetes deployment render") {
		t.Fatalf("hyperdrive on Kubernetes render accepted: %v", err)
	}
}
