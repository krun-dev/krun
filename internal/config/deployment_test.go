package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProductionFixtureLoadsThroughMainConfig(t *testing.T) {
	setExampleCredentials(t)
	c, err := LoadConfig(filepath.Join("testdata", "production.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Deployment == nil || c.Development || c.StaticDir != "" || c.Workers.BuilderMode != "kubernetes" || c.Workers.Domains[0].Access != "private" {
		t.Fatal("production example has incompatible topology")
	}
	if c.Deployment.RuntimeReplicas != 2 || c.Deployment.ControlReplicas != 1 {
		t.Fatal("incorrect initial replicas")
	}
	for _, filename := range []string{"example.yaml"} {
		raw, err := os.ReadFile(filepath.Join("..", "..", "config", filename))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), "# deployment:") {
			t.Fatal("main configuration examples omit deployment inputs")
		}
	}
}

func TestLoadConfigRestrictsLocalBuilderToDevelopment(t *testing.T) {
	setExampleCredentials(t)
	raw, err := os.ReadFile(filepath.Join("testdata", "production.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	local := strings.Replace(string(raw), "builder_mode: kubernetes", "builder_mode: local", 1)
	for _, tc := range []struct {
		name        string
		development string
		allowed     bool
	}{
		{"production", "development: false", false},
		{"default", "", false},
		{"development", "development: true", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "server.yaml")
			yaml := strings.Replace(local, "development: false", tc.development, 1)
			if tc.allowed {
				t.Setenv("KRUN_PUBLIC_URL", "http://127.0.0.1:8080")
				yaml = strings.ReplaceAll(yaml, "0.0.0.0:", "127.0.0.1:")
			}
			if err := os.WriteFile(path, []byte(yaml), 0600); err != nil {
				t.Fatal(err)
			}
			c, err := LoadConfig(path)
			if !tc.allowed {
				if err == nil || !strings.Contains(err.Error(), "workers.builder_mode: local requires development: true") {
					t.Fatalf("expected a development-only builder error, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if c.Workers.BuilderMode != "local" || c.Workers.BuilderScript == "" {
				t.Fatal("local builder or its default script was not loaded")
			}
		})
	}
}

func TestDeploymentValidationRejectsUnboundedOrInjectedInput(t *testing.T) {
	setExampleCredentials(t)
	for _, test := range []struct {
		name   string
		change func(*DeploymentConfig)
	}{
		{"unresolved DNS", func(d *DeploymentConfig) { d.DNSResolver = "dns.example.com" }},
		{"untagged image with registry port", func(d *DeploymentConfig) { d.ControlImage = "registry.example.com:5000/krun" }},
		{"unrestricted CIDR", func(d *DeploymentConfig) {
			d.Network.Builder = []DeploymentEgress{{CIDRs: []string{"0.0.0.0/0"}, Ports: []int{443}}}
		}},
		{"mixed public and private", func(d *DeploymentConfig) {
			d.Network.Runtime = []DeploymentEgress{{PublicHTTPS: true, CIDRs: []string{"192.0.2.0/24"}}}
		}},
		{"empty egress", func(d *DeploymentConfig) { d.Network.Control = nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			c, err := LoadConfig(filepath.Join("testdata", "production.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			test.change(c.Deployment)
			if err = c.Validate(); err == nil {
				t.Fatal("invalid deployment configuration accepted")
			}
		})
	}
}

func TestThreeGatewayFixtureLoadsThroughMainConfig(t *testing.T) {
	setExampleCredentials(t)
	c, err := LoadConfig(filepath.Join("testdata", "three-gateways.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Deployment.ConsoleGateway != "office" || len(c.Workers.Gateways) != 3 || len(c.Workers.RuntimePools) != 3 {
		t.Fatal("incorrect three-entry test topology")
	}
	for _, profile := range c.Workers.NetworkProfiles {
		domains := c.Workers.DomainsForProfile(profile)
		pools := c.Workers.PoolsForProfile(profile.ID)
		if len(domains) != 1 || len(pools) != 1 || domains[0].Gateway != profile.ID || pools[0].Labels["exposure"] != profile.ID {
			t.Fatal("profile does not isolate its gateway and pool")
		}
	}
}

func TestPoolDeploymentValidation(t *testing.T) {
	setExampleCredentials(t)
	cfg, err := LoadConfig(filepath.Join("testdata", "production.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	pool := cfg.Workers.RuntimePools[0]
	if d := pool.Deployment; d == nil || d.Image != cfg.Deployment.RuntimeImage ||
		d.Replicas != cfg.Deployment.RuntimeReplicas || d.SecretName != pool.RuntimeService ||
		d.PeerService != pool.DefaultPeerService() {
		t.Fatalf("pool did not inherit its deployment defaults: %+v", d)
	}
	for _, test := range []struct {
		field  string
		change func(*RuntimePoolDeployment)
	}{
		{"pod_labels", func(d *RuntimePoolDeployment) { d.PodLabels = map[string]string{"app": "conflict"} }},
		{"config_map_annotations", func(d *RuntimePoolDeployment) { d.ConfigMapAnnotations = map[string]string{"bad key": "value"} }},
		{"config_map_annotations", func(d *RuntimePoolDeployment) {
			d.ConfigMapAnnotations = map[string]string{"example.com/key": strings.Repeat("x", 256*1024)}
		}},
		{"node_selector", func(d *RuntimePoolDeployment) { d.NodeSelector = map[string]string{"invalid key": "node"} }},
		{"tolerations", func(d *RuntimePoolDeployment) { d.Tolerations = []BuilderToleration{{Operator: "Equal"}} }},
		{"dns_policy", func(d *RuntimePoolDeployment) { d.DNSPolicy = "Invalid" }},
		{"dns_config.nameservers", func(d *RuntimePoolDeployment) { d.DNSPolicy = "None" }},
		{"dns_config.nameservers", func(d *RuntimePoolDeployment) {
			d.DNSConfig = &RuntimePodDNSConfig{Nameservers: []string{"resolver.example.com"}}
		}},
		{"dns_config.searches", func(d *RuntimePoolDeployment) { d.DNSConfig = &RuntimePodDNSConfig{Searches: []string{"-invalid"}} }},
		{"peer_service", func(d *RuntimePoolDeployment) { d.PeerService = pool.ValidatorService }},
	} {
		t.Run(test.field, func(t *testing.T) {
			d := *pool.Deployment
			test.change(&d)
			cfg.Workers.RuntimePools[0].Deployment = &d
			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), test.field) {
				t.Fatalf("want error for %s, got %v", test.field, err)
			}
		})
	}
}

func TestPoolConfigMapAnnotationsLoadFromYAML(t *testing.T) {
	setExampleCredentials(t)
	raw, err := os.ReadFile(filepath.Join("testdata", "production.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	content := strings.Replace(string(raw), "deployment: {}", "deployment:\n        config_map_annotations:\n          kubeocean.io/sync-to-physical: \"true\"", 1)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Workers.RuntimePools[0].Deployment.ConfigMapAnnotations["kubeocean.io/sync-to-physical"]; got != "true" {
		t.Fatalf("annotation = %q, want true", got)
	}
}

// The key-policy examples describe a different document type, not server configuration.
func TestOnlyOneServerConfigExample(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "config", "*.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		switch filepath.Base(file) {
		case "key-policy.example.yaml", "key-policy.wildcard.example.yaml":
		default:
			t.Errorf("server configuration examples must use config/example.yaml only: %s", file)
		}
	}
	if _, err := os.Stat(filepath.Join("..", "..", "config", "example.yaml")); err != nil {
		t.Fatal(err)
	}
}
