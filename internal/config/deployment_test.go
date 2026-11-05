package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProductionExampleLoadsThroughMainConfig(t *testing.T) {
	c, err := LoadConfig(filepath.Join("..", "..", "config", "production.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Deployment == nil || c.Development || c.StaticDir != "" || c.Workers.BuilderMode != "kubernetes" || c.Workers.Domains[0].Access != "private" {
		t.Fatal("production example has incompatible topology")
	}
	if c.Deployment.RuntimeReplicas != 2 || c.Deployment.ControlReplicas != 1 {
		t.Fatal("incorrect initial replicas")
	}
	for _, filename := range []string{"example.yaml", "development.example.yaml"} {
		raw, err := os.ReadFile(filepath.Join("..", "..", "config", filename))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), "# deployment:") {
			t.Fatal("main configuration examples omit deployment inputs")
		}
	}
}
func TestDeploymentValidationRejectsUnboundedOrInjectedInput(t *testing.T) {
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
			c, err := LoadConfig(filepath.Join("..", "..", "config", "production.example.yaml"))
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

func TestThreeGatewayExampleLoadsThroughMainConfig(t *testing.T) {
	c, err := LoadConfig(filepath.Join("..", "..", "config", "test.example.yaml"))
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
