package config

import (
	"strings"
	"testing"

	"github.com/krun-dev/krun/internal/configfile"
)

func TestArtifactStorageSTSConfiguration(t *testing.T) {
	const input = `
artifact_storage:
  endpoint: https://tos-s3-cn-beijing.ivolces.com
  log_read_endpoint: https://tos-s3-cn-beijing.volces.com
  region: cn-beijing
  bucket: artifacts
  prefix: company/test/artifacts
  credentials:
    mode: sts
    provider: volcengine
    access_key_id: example-issuer
    secret_access_key: example-secret
    role_trn: trn:iam::1000000000:role/example
`
	load := func(raw string) (WorkersConfig, error) {
		var cfg WorkersConfig
		if err := configfile.Decode("workers.yaml", []byte(raw), &cfg); err != nil {
			return cfg, err
		}
		return cfg, cfg.validateArtifactStorage()
	}
	cfg, err := load(input)
	if err != nil {
		t.Fatal(err)
	}
	store := cfg.ArtifactStorage
	if !cfg.ArtifactUsesSTS() || cfg.ArtifactCredentials().Type != "tos_assume_role" ||
		store.BuilderEndpoint != store.Endpoint || store.RuntimeEndpoint != store.Endpoint ||
		store.AddressingStyle != "virtual" || store.BucketMode != "external" ||
		store.ConfigBaseURL != "https://artifacts.tos-s3-cn-beijing.ivolces.com/company/test/artifacts/config" {
		t.Fatal("STS credentials or endpoint defaults did not resolve")
	}
	for _, test := range []struct{ name, input string }{
		{"mixed layout", input + "s3_endpoint: https://s3.example.com\n"},
		{"legacy flat layout", "s3_endpoint: https://s3.example.com\n"},
		{"legacy runtime credentials", input + "s3_runtime_credentials:\n  type: static\n"},
		{"unknown field", input + "  unexpected: true\n"},
		{"missing mode", strings.Replace(input, "    mode: sts\n", "", 1)},
		{"unknown provider", strings.Replace(input, "provider: volcengine", "provider: other", 1)},
		{"managed bucket", input + "  bucket_mode: managed\n"},
		{"missing prefix", strings.Replace(input, "  prefix: company/test/artifacts\n", "", 1)},
		{"external signer target", strings.Replace(input, "https://tos-s3-cn-beijing.volces.com", "https://other.example.com", 1)},
		{"mixed credential modes", strings.Replace(input, "mode: sts", "mode: ak_sk", 1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := load(test.input); err == nil {
				t.Fatal("unsupported configuration was accepted")
			}
		})
	}
}
