package control

import (
	"strings"
	"testing"

	"github.com/krun-dev/krun/internal/config"
)

func TestConsoleCSPRestrictsDirectTransfersToConfiguredOrigins(t *testing.T) {
	s := &Server{Config: config.Config{Workers: &config.WorkersConfig{ObjectStorage: &config.WorkerObjectStorageConfig{
		Stores: []config.WorkerObjectStore{
			{Endpoint: "https://tos-s3-cn-beijing.volces.com", Bucket: "example-worker-data", AddressingStyle: "virtual"},
			{Endpoint: "http://127.0.0.1:9000", Bucket: "example-worker-data", AddressingStyle: "path"},
		},
	}}}}
	policy := s.consoleContentSecurityPolicy()
	if !strings.Contains(policy, "connect-src 'self' https://example-worker-data.tos-s3-cn-beijing.volces.com http://127.0.0.1:9000;") ||
		strings.Contains(policy, "*") || !strings.Contains(policy, "script-src 'self';") {
		t.Fatal("CSP omitted the exact store origin or widened script access")
	}
}
