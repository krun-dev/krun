package config

import (
	"encoding/json"
	"os"
	"path/filepath"
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

func TestWorkersAllowDirectDatabaseConnectionsWithoutProxyConfiguration(t *testing.T) {
	setExampleCredentials(t)
	t.Setenv("KRUN_DATABASE_URL", "postgres://fixture/krun_test")
	cfg, err := LoadConfig(filepath.Join("..", "..", "config", "example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Workers == nil || cfg.Listen != "0.0.0.0:8080" || cfg.InternalListen != "0.0.0.0:8081" {
		t.Fatal("Workers should load with standard HTTP listeners and no database proxy")
	}
}
