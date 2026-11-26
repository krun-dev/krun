package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuilderJobConfigLoad(t *testing.T) {
	setExampleCredentials(t)
	t.Setenv("KRUN_DATABASE_URL", "postgres://fixture/krun_test")
	raw, err := os.ReadFile(filepath.Join("..", "..", "config", "example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Replace(string(raw), "builder_node_selector: {}", "builder_node_selector: {workload: build}", 1)
	text = strings.Replace(text, "builder_tolerations: []", "builder_tolerations: [{key: workload, operator: Equal, value: build, effect: NoSchedule}]", 1)
	text = strings.Replace(text, `cpu: "2", memory: "2Gi"`, `cpu: "4", memory: "8Gi"`, 1)
	file := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(file, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(file)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Workers.BuilderBuildResources.Limits.CPU != "4" || cfg.Workers.BuilderBuildResources.Limits.Memory != "8Gi" || cfg.Workers.BuilderNodeSelector["workload"] != "build" || len(cfg.Workers.BuilderTolerations) != 1 {
		t.Fatalf("builder config not loaded: %#v", cfg.Workers)
	}
}

func TestBuilderJobValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config WorkersConfig
	}{
		{"bad quantity", WorkersConfig{BuilderBuildResources: BuilderResources{Limits: BuilderResourceValues{CPU: "wrong"}}}},
		{"request exceeds limit", WorkersConfig{BuilderPackResources: BuilderResources{Requests: BuilderResourceValues{Memory: "1Gi"}}}},
		{"invalid selector", WorkersConfig{BuilderNodeSelector: map[string]string{"bad key": "value"}}},
		{"invalid input mode", WorkersConfig{BuilderInputMode: "invalid"}},
		{"invalid toleration", WorkersConfig{BuilderTolerations: []BuilderToleration{{Key: "workload", Operator: "Exists", Value: "build"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.config.validateBuilderJob(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
	c := WorkersConfig{BuilderBuildResources: BuilderResources{Limits: BuilderResourceValues{Memory: "4Gi"}}}
	if err := c.validateBuilderJob(); err != nil {
		t.Fatal(err)
	}
	if c.BuilderBuildResources.Limits.Memory != "4Gi" || c.BuilderBuildResources.Requests.CPU != "100m" {
		t.Fatal("partial overrides did not retain defaults")
	}
}
