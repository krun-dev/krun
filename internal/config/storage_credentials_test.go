package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func objectStorageExample() *WorkerObjectStorageConfig {
	return &WorkerObjectStorageConfig{
		DefaultStore: "primary",
		Stores: []WorkerObjectStore{{
			ID: "primary", Endpoint: "https://tos-s3-cn-beijing.volces.com", Region: "cn-beijing",
			Bucket: "example-worker-data", RootPrefix: "worker-data/v1", AddressingStyle: "virtual",
			Credentials: WorkerStorageCredentialConfig{Type: "tos_assume_role", AccessKeyID: "example-access", SecretAccessKey: "example-secret", RoleTrn: "trn:iam::1000000000:role/example"},
		}},
	}
}

func TestObjectStorageValidation(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*WorkerObjectStorageConfig)
	}{
		{"unknown provider", func(c *WorkerObjectStorageConfig) { c.Stores[0].Credentials.Type = "aws_assume_role" }},
		{"missing role", func(c *WorkerObjectStorageConfig) { c.Stores[0].Credentials.RoleTrn = "" }},
		{"invalid role", func(c *WorkerObjectStorageConfig) { c.Stores[0].Credentials.RoleTrn = "role/example" }},
		{"credential exfiltration endpoint", func(c *WorkerObjectStorageConfig) { c.Stores[0].Credentials.STSEndpoint = "https://other.example" }},
		{"wrong S3 endpoint", func(c *WorkerObjectStorageConfig) { c.Stores[0].Endpoint = "https://tos-cn-beijing.volces.com" }},
		{"path TOS", func(c *WorkerObjectStorageConfig) { c.Stores[0].AddressingStyle = "path" }},
		{"wildcard prefix", func(c *WorkerObjectStorageConfig) { c.Stores[0].RootPrefix = "worker-data/*" }},
		{"empty prefix", func(c *WorkerObjectStorageConfig) { c.Stores[0].RootPrefix = "" }},
		{"duplicate store", func(c *WorkerObjectStorageConfig) { c.Stores = append(c.Stores, c.Stores[0]) }},
		{"unknown default", func(c *WorkerObjectStorageConfig) { c.DefaultStore = "missing" }},
		{"short TTL", func(c *WorkerObjectStorageConfig) { c.CredentialTTLSeconds = 899 }},
		{"bad refresh", func(c *WorkerObjectStorageConfig) { c.RefreshBeforeSeconds = 3600 }},
		{"unbounded concurrency", func(c *WorkerObjectStorageConfig) { c.CredentialConcurrency = 129 }},
		{"missing key", func(c *WorkerObjectStorageConfig) { c.Stores[0].Credentials.SecretAccessKey = "" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := objectStorageExample()
			test.mutate(c)
			if err := c.Validate(); err == nil {
				t.Fatal("expected invalid configuration")
			}
		})
	}
	c := objectStorageExample()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if c.CredentialTTLSeconds != 3600 || c.RefreshBeforeSeconds != 300 || c.CredentialCacheEntries != 1024 {
		t.Fatal("incorrect defaults")
	}
}

func TestObjectStorageExamplesLoadThroughMainConfig(t *testing.T) {
	t.Setenv("KRUN_DATABASE_URL", "postgres://test-only/krun_test")
	for _, name := range []string{"example.yaml", "development.example.yaml"} {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("..", "..", "config", name))
			if err != nil {
				t.Fatal(err)
			}
			var section []string
			active := false
			for _, line := range strings.Split(string(raw), "\n") {
				if strings.TrimSpace(line) == "# object_storage:" {
					active = true
				}
				if !active {
					continue
				}
				if !strings.HasPrefix(line, "  # ") {
					break
				}
				text := strings.TrimPrefix(line, "  # ")
				if len(section) > 0 && !strings.HasPrefix(text, " ") {
					break
				}
				section = append(section, "  "+text)
			}
			if len(section) < 10 {
				t.Fatal("missing usable object_storage example")
			}
			content := string(raw)
			if name == "development.example.yaml" {
				base, err := os.ReadFile(filepath.Join("..", "..", "config", "example.yaml"))
				if err != nil {
					t.Fatal(err)
				}
				content = string(base)
			}
			// Append beneath the main workers mapping, before its repository list.
			content = strings.Replace(content, "  repository_providers:", strings.Join(section, "\n")+"\n  repository_providers:", 1)
			filename := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(filename, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			loaded, err := LoadConfig(filename)
			if err != nil {
				t.Fatal(err)
			}
			if loaded.Workers.ObjectStorage == nil || len(loaded.Workers.ObjectStorage.Stores) != 1 {
				t.Fatal("object_storage not loaded")
			}
		})
	}
}
