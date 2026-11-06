package deployment

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/krun-dev/krun/internal/config"
)

func fixture(t *testing.T) config.Config {
	t.Helper()
	t.Setenv("KRUN_CONTROL_IMAGE", "registry.example.com/krun/control:fixture")
	t.Setenv("KRUN_RUNTIME_IMAGE", "registry.example.com/krun/runtime:fixture")
	t.Setenv("KRUN_BUILDER_IMAGE", "registry.example.com/krun/builder:fixture")
	t.Setenv("KRUN_WORKERS_SECRETS_KEY", "ERERERERERERERERERERERERERERERERERERERERERE=")
	t.Setenv("KRUN_OBJECT_STORAGE_CURSOR_KEY", strings.Repeat("B", 43))
	t.Setenv("KRUN_OIDC_CLIENT_ID", "fixture-client")
	t.Setenv("KRUN_OIDC_SECRET", "fixture-client-secret")
	t.Setenv("KRUN_GITLAB_CLIENT_ID", "fixture-gitlab-client")
	t.Setenv("KRUN_GITLAB_CLIENT_SECRET", "fixture-gitlab-secret")
	t.Setenv("KRUN_DATABASE_URL", "postgres://krun:fixture-password@database.example.com/krun")
	t.Setenv("KRUN_ARTIFACT_ACCESS", "fixture-artifact-access")
	t.Setenv("KRUN_ARTIFACT_SECRET", "fixture-artifact-secret")
	t.Setenv("KRUN_RUNTIME_TOKEN", "fixture-serving-token-long-enough-for-tests")
	t.Setenv("KRUN_MANAGEMENT_TOKEN", "fixture-management-token-long-enough-for-tests")
	t.Setenv("KRUN_OBJECT_STORAGE_ACCESS", "fixture-issuer-access")
	t.Setenv("KRUN_OBJECT_STORAGE_SECRET", "fixture-issuer-secret")
	c, err := config.LoadConfig(filepath.Join("..", "..", "..", "config", "production.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func TestProductionResourcesPreserveConfigAndIsolateCredentials(t *testing.T) {
	c := fixture(t)
	c.Workers.ValidatorPool = c.Workers.RuntimePools[0].ID
	// Credentials containing dollar expressions must survive the container's
	// second LoadConfig pass without consulting unrelated environment values.
	c.Workers.S3SecretKey = "artifact-secret-${literal}-$token"
	c.Workers.ObjectStorage.Stores[0].Credentials.SecretAccessKey = "issuer-secret-must-not-reach-runtime"
	c.Workers.ObjectStorage.Stores[0].Delivery = &config.WorkerObjectDeliveryConfig{
		Type:       "volc_cdn_a",
		BaseURL:    "https://files.example.com",
		SigningKey: strings.Repeat("fixture-cdn", 4),
		TTLSeconds: 300,
	}
	out := t.TempDir()
	if err := Render(c, out); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(out, "resources.json"))
	if err != nil {
		t.Fatal(err)
	}
	var list struct{ Items []map[string]any }
	if err = json.Unmarshal(raw, &list); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	var main string
	for _, resource := range list.Items {
		kind := resource["kind"].(string)
		counts[kind]++
		name := resource["metadata"].(map[string]any)["name"].(string)
		if name == "krun-console" && kind != "HTTPRoute" {
			t.Fatal("renderer provisioned a frontend proxy resource")
		}
		if kind == "HTTPRoute" {
			rules := resource["spec"].(map[string]any)["rules"].([]any)
			if len(rules) != 1 {
				t.Fatal("control route contains a static catch-all")
			}
			rule := rules[0].(map[string]any)
			match := rule["matches"].([]any)[0].(map[string]any)
			backend := rule["backendRefs"].([]any)[0].(map[string]any)
			if match["path"].(map[string]any)["value"] != "/api" || backend["name"] != "krun-control" {
				t.Fatal("control route must forward only the API prefix")
			}
		}
		if kind == "Gateway" || kind == "PersistentVolumeClaim" || kind == "ClusterRole" {
			t.Fatal("renderer provisioned company-owned infrastructure")
		}
		if kind == "ConfigMap" {
			encoded, _ := json.Marshal(resource)
			for _, secret := range []string{
				c.Workers.S3SecretKey,
				c.Workers.ObjectStorage.Stores[0].Credentials.SecretAccessKey,
				c.Workers.ObjectStorage.Stores[0].Delivery.SigningKey,
			} {
				if strings.Contains(string(encoded), secret) {
					t.Fatal("credential appeared in a ConfigMap")
				}
			}
		}
		if kind == "Secret" {
			data := map[string]any{}
			for key, encoded := range resource["data"].(map[string]any) {
				raw, err := base64.StdEncoding.DecodeString(encoded.(string))
				if err != nil {
					t.Fatal(err)
				}
				data[key] = string(raw)
			}
			if name == "krun-control-config" {
				main = data["control.yaml"].(string)
				continue
			}
			encoded, _ := json.Marshal(data)
			if strings.Contains(string(encoded), "issuer-secret-must-not-reach-runtime") || strings.Contains(string(encoded), "fixture-cdn") {
				t.Fatal("business storage issuer/CDN credentials reached runtime")
			}
			if name == c.Workers.RuntimePools[0].ValidatorService {
				if data["artifact-secret"] != nil || data["KRUN_RUNTIME_TOKEN"] == c.Workers.RuntimePools[0].RuntimeToken {
					t.Fatal("validator received serving credentials")
				}
			}
		}
		if kind == "Deployment" {
			spec := resource["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
			if name != "krun-control" && spec["automountServiceAccountToken"] != false {
				t.Fatal("non-control pod received API credentials")
			}
			if name == "krun-control" && spec["automountServiceAccountToken"] != true {
				t.Fatal("controller cannot use its namespace-scoped service account")
			}
		}
	}
	if counts["Deployment"] != 3 || counts["HTTPRoute"] != 1 || counts["NetworkPolicy"] != 6 {
		t.Fatalf("unexpected topology: %+v", counts)
	}
	path := filepath.Join(t.TempDir(), "control.yaml")
	if err = os.WriteFile(path, []byte(main), 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.LoadConfig(path)
	if err != nil || loaded.Workers.S3SecretKey != c.Workers.S3SecretKey {
		t.Fatal("rendered main configuration did not round-trip resolved values", err)
	}
	if mode, _ := os.Stat(out); mode.Mode().Perm() != 0700 {
		t.Fatal("private output directory permissions")
	}
	if mode, _ := os.Stat(filepath.Join(out, "resources.json")); mode.Mode().Perm() != 0600 {
		t.Fatal("private manifest permissions")
	}
	summary, _ := os.ReadFile(filepath.Join(out, "summary.json"))
	if strings.Contains(string(summary), "artifact-secret") || strings.Contains(string(summary), "issuer-secret") {
		t.Fatal("private configuration leaked into summary")
	}
}

func TestProductionDeploymentRejectsDevelopmentAndAmbiguousTargets(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*config.Config)
	}{
		{"development", func(c *config.Config) { c.Development = true }},
		{"local builder", func(c *config.Config) { c.Workers.BuilderMode = "local" }},
		{"in-cluster fixture image", func(c *config.Config) { c.Workers.BuilderPullPolicy = "Never" }},
		{"loopback listener", func(c *config.Config) { c.Listen = "127.0.0.1:8080" }},
		{"shared namespace", func(c *config.Config) { c.Workers.Namespace = "default" }},
		{"local kube token", func(c *config.Config) { c.Workers.KubernetesTokenFile = "/local/token" }},
		{"external kubeconfig", func(c *config.Config) {
			c.Workers.KubernetesAPI = ""
			c.Workers.KubernetesTokenFile = ""
			c.Workers.KubernetesCAFile = ""
			c.Workers.KubernetesKubeconfigFile = "/credentials/kubeconfig"
		}},
		{
			"tunnel",
			func(c *config.Config) { c.Workers.RuntimePools[0].ValidatorManagementBase = "http://127.0.0.1:38084" },
		},
		{"managed bucket", func(c *config.Config) { c.Workers.S3BucketMode = "managed" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := fixture(t)
			test.change(&c)
			if err := Render(c, t.TempDir()); err == nil {
				t.Fatal("unsafe/incompatible deployment accepted")
			}
		})
	}
}
func TestExistingGatewayPreflight(t *testing.T) {
	c := fixture(t)
	good := `{"metadata":{"name":"krun-internal","namespace":"kgateway-system"},"spec":{"listeners":[{"name":"internal","protocol":"HTTP","port":8080,"allowedRoutes":{"namespaces":{"from":"Selector","selector":{"matchLabels":{"app.kubernetes.io/part-of":"krun"}}}}}]},"status":{"conditions":[{"type":"Programmed","status":"True"}]}}`
	if err := CheckGateway(c, []byte(good)); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		strings.Replace(good, `"port":8080`, `"port":443`, 1),
		strings.Replace(good, `"krun"`, `"another-app"`, 1),
		strings.Replace(good, `"status":"True"`, `"status":"False"`, 1),
	} {
		if err := CheckGateway(c, []byte(raw)); err == nil {
			t.Fatal("incompatible Gateway accepted")
		}
	}
}
