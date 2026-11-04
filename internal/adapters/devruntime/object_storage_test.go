package devruntime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/krun-dev/krun/internal/config"
)

func TestObjectStoragePinsTransportWithoutRenderingIssuerCredentials(t *testing.T) {
	t.Chdir(filepath.Join("..", "..", ".."))
	cfg := config.Config{InternalListen: "0.0.0.0:9001", Workers: &config.WorkersConfig{
		S3RuntimeEndpoint: "http://192.0.2.20:9000", ConfigBaseURL: "http://192.0.2.20:9000/artifacts/config",
		RuntimePools:  []config.WorkerRuntimePool{{ID: "default", Default: true, RuntimeToken: "fixture-serving", ValidatorToken: "fixture-validator", ManagementToken: "fixture-management"}},
		ObjectStorage: &config.WorkerObjectStorageConfig{DefaultStore: "data", Stores: []config.WorkerObjectStore{{ID: "data", Endpoint: "https://Storage.Example.com:443", AddressingStyle: "virtual", Bucket: "physical-data", Credentials: config.WorkerStorageCredentialConfig{SecretAccessKey: "issuer-must-stay-in-control-plane"}}}},
	}}
	out := t.TempDir()
	if err := Render(cfg, "192.0.2.10", out); err != nil {
		t.Fatal(err)
	}
	runtime, err := os.ReadFile(filepath.Join(out, "runtime.capnp"))
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{`certificateHost = "physical-data.storage.example.com"`, `value = "physical-data.storage.example.com"`, `address = "physical-data.storage.example.com:443"`, `name = "OBJECT_STORES"`, `name = "CACHE_SOURCE"`, `name = "cache.js"`} {
		if !strings.Contains(string(runtime), part) {
			t.Fatal("missing pinned storage transport:", part)
		}
	}
	validator, _ := os.ReadFile(filepath.Join(out, "validator.capnp"))
	if !strings.Contains(string(validator), `name = "CACHE_SOURCE"`) {
		t.Fatal("validator is missing the isolated Cache API facade")
	}
	if strings.Contains(string(validator), `service = "object-store-data"`) {
		t.Fatal("validator received object transport")
	}
	secrets, _ := os.ReadFile(filepath.Join(out, "secrets.json"))
	if !strings.Contains(string(secrets), "KRUN_VALIDATOR_TOKEN") || strings.Contains(string(secrets), "issuer-must-stay-in-control-plane") {
		t.Fatal("incorrect secret delivery")
	}
	policy, _ := os.ReadFile(filepath.Join(out, "networkpolicy.json"))
	if !strings.Contains(string(policy), `"port": 443`) {
		t.Fatal("missing object storage TLS egress")
	}
}
