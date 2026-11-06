package devruntime

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/krun-dev/krun/internal/config"
)

func TestExternalRenderSeparatesRuntimeCredentialsAndEnablesVerifiedTLS(t *testing.T) {
	out := t.TempDir()
	t.Chdir(filepath.Join("..", "..", ".."))
	cfg := config.Config{
		InternalListen: "0.0.0.0:9001",
		Workers: &config.WorkersConfig{
			ValidatorPool:     "default",
			S3RuntimeEndpoint: "https://storage.example.com",
			S3BucketMode:      "external",
			S3Region:          "cn-beijing",
			ConfigBaseURL:     "https://bucket.storage.example.com/dev/run/config",
			S3AccessKey:       "fixture-access",
			S3SecretKey:       "fixture-secret",
			RuntimePools: []config.WorkerRuntimePool{
				{
					ID:              "default",
					Default:         true,
					Access:          []string{"private", "public"},
					RuntimeToken:    "fixture-runtime-token",
					ManagementToken: "fixture-management-token",
				},
			},
			Domains: []config.WorkerDomain{{StaticDomain: "static.example.com", Access: "public"}},
		},
	}
	if err := Render(cfg, "192.0.2.10", out); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(out, "secrets.json"))
	if err != nil {
		t.Fatal(err)
	}
	var secrets struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if err = json.Unmarshal(raw, &secrets); err != nil {
		t.Fatal(err)
	}
	if len(secrets.Items) != 2 {
		t.Fatal("expected separate runtime and storage secrets")
	}
	names := map[string]bool{}
	for _, secret := range secrets.Items {
		names[secret.Metadata.Name] = true
	}
	if names["workers-credentials"] || !names["workers-runtime"] || !names["workers-storage"] {
		t.Fatal("renderer must never replace the shared infrastructure secret")
	}
	for _, name := range []string{"runtime.capnp", "validator.capnp", "networkpolicy.json"} {
		b, err := os.ReadFile(filepath.Join(out, name))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "fixture-secret") || strings.Contains(string(b), "fixture-access") {
			t.Fatal("credentials leaked outside Secret manifest")
		}
		if strings.Contains(string(b), "__") {
			t.Fatal("unrendered infrastructure placeholder")
		}
	}
	runtime, _ := os.ReadFile(filepath.Join(out, "runtime.capnp"))
	if !regexp.MustCompile(`trustBrowserCas\s*=\s*true`).Match(runtime) || !strings.Contains(string(runtime), "/dev/run/config/pools/default") || !strings.Contains(string(runtime), "/storage/secret") {
		t.Fatal("runtime missing private storage or TLS wiring")
	}
	patch, _ := os.ReadFile(filepath.Join(out, "runtime-patch.json"))
	if !strings.Contains(string(patch), `"secretName": "workers-runtime"`) {
		t.Fatal("runtime must use its dedicated secret")
	}
	info, err := os.Stat(filepath.Join(out, "secrets.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("secret manifest must be owner-only")
	}
	if _, err = os.Stat(filepath.Join(out, "static-assets.yaml")); !os.IsNotExist(err) {
		t.Fatal("external storage must not render an obsolete MinIO origin")
	}
}

func TestRenderStorageModes(t *testing.T) {
	t.Chdir(filepath.Join("..", "..", ".."))

	for _, test := range []struct {
		name       string
		endpoint   string
		accessMode string
		bucketMode string
		wantCIDR   string
		wantPort   int
	}{
		{"local managed direct", "http://192.0.2.20:9000", "direct", "managed", "192.0.2.20/32", 9000},
		{"private existing signed", "http://192.0.2.20:9000", "signed", "external", "192.0.2.20/32", 9000},
		{"private TLS signed", "https://192.0.2.20:9443", "signed", "external", "192.0.2.20/32", 9443},
		{"public managed signed", "https://storage.example.com:9443", "signed", "managed", "0.0.0.0/0", 9443},
		{"public existing signed", "https://storage.example.com", "signed", "external", "0.0.0.0/0", 443},
	} {
		t.Run(test.name, func(t *testing.T) {
			out := t.TempDir()
			cfg := config.Config{
				InternalListen: "0.0.0.0:9001",
				Workers: &config.WorkersConfig{
					ValidatorPool:     "default",
					S3RuntimeEndpoint: test.endpoint,
					S3AccessMode:      test.accessMode,
					S3BucketMode:      test.bucketMode,
					S3Region:          "us-east-1",
					ConfigBaseURL:     test.endpoint + "/bucket/config",
					RuntimePools: []config.WorkerRuntimePool{
						{ID: "default", Default: true},
					},
				},
			}
			if err := Render(cfg, "192.0.2.10", out); err != nil {
				t.Fatal(err)
			}

			runtime, err := os.ReadFile(filepath.Join(out, "runtime.capnp"))
			if err != nil {
				t.Fatal(err)
			}
			signed := test.accessMode == "signed"
			if strings.Contains(string(runtime), "STORAGE_SECRET_KEY") != signed {
				t.Fatal("runtime storage credentials do not match access mode")
			}
			_, staticErr := os.Stat(filepath.Join(out, "static-assets.yaml"))
			if signed != os.IsNotExist(staticErr) {
				t.Fatal("static origin does not match access mode")
			}

			raw, err := os.ReadFile(filepath.Join(out, "networkpolicy.json"))
			if err != nil {
				t.Fatal(err)
			}
			var policies struct {
				Items []struct {
					Spec struct {
						Egress []renderedEgressRule `json:"egress"`
					} `json:"spec"`
				} `json:"items"`
			}
			if err := json.Unmarshal(raw, &policies); err != nil {
				t.Fatal(err)
			}
			for _, policy := range policies.Items {
				found := false
				for _, rule := range policy.Spec.Egress {
					if len(rule.To) == 1 && rule.To[0].IPBlock.CIDR == test.wantCIDR &&
						len(rule.Ports) == 1 && rule.Ports[0].Port == test.wantPort {
						found = true
					}
				}
				if !found {
					t.Fatalf("storage egress missing %s:%d", test.wantCIDR, test.wantPort)
				}
			}
		})
	}
}

// Only the destination and port matter for storage egress assertions.
type renderedEgressRule struct {
	To []struct {
		IPBlock struct {
			CIDR string `json:"cidr"`
		} `json:"ipBlock"`
	} `json:"to"`
	Ports []struct {
		Port int `json:"port"`
	} `json:"ports"`
}

func TestCheckStorageUsesStandardS3BucketRequests(t *testing.T) {
	for _, test := range []struct {
		name       string
		bucketMode string
		status     int
		wantError  bool
	}{
		{"existing external bucket", "external", http.StatusOK, false},
		{"missing managed bucket", "managed", http.StatusNotFound, false},
		{"missing external bucket", "external", http.StatusNotFound, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Method != http.MethodHead || r.URL.Path != "/bucket/" {
					t.Errorf("unexpected storage probe: %s %s", r.Method, r.URL.Path)
				}
				w.WriteHeader(test.status)
			}))
			defer server.Close()

			cfg := &config.WorkersConfig{
				S3Endpoint:        server.URL,
				S3Bucket:          "bucket",
				S3Region:          "us-east-1",
				S3AddressingStyle: "path",
				S3BucketMode:      test.bucketMode,
				S3AccessKey:       "fixture-access",
				S3SecretKey:       "fixture-secret",
			}
			if err := CheckStorage(cfg); (err != nil) != test.wantError {
				t.Fatalf("storage probe error = %v, want error = %v", err, test.wantError)
			}
			if requests != 1 {
				t.Fatalf("got %d storage probes, want one S3 request", requests)
			}
		})
	}
}
