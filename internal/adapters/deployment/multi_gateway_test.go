package deployment

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/krun-dev/krun/internal/config"
)

func multiFixture(t *testing.T) config.Config {
	t.Helper()
	fixture(t)
	for _, zone := range []string{"internal", "office", "public"} {
		prefix := "KRUN_" + strings.ToUpper(zone)
		t.Setenv(prefix+"_RUNTIME_TOKEN", "fixture-serving-token-long-enough-"+zone)
		t.Setenv(prefix+"_MANAGEMENT_TOKEN", "fixture-management-token-long-enough-"+zone)
	}
	c, err := config.LoadConfig(filepath.Join("..", "..", "..", "config", "test.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestThreeGatewaysKeepPoolsAndConsoleIsolated(t *testing.T) {
	c := multiFixture(t)
	out := t.TempDir()
	if err := Render(c, out); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(out, "resources.json"))
	if err != nil {
		t.Fatal(err)
	}
	var list struct{ Items []map[string]any }
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	deployments := 0
	for _, item := range list.Items {
		kind := item["kind"].(string)
		meta := item["metadata"].(map[string]any)
		name := meta["name"].(string)
		key := kind + "/" + name
		if seen[key] {
			t.Fatalf("duplicate resource %s", key)
		}
		seen[key] = true
		if kind == "Gateway" {
			t.Fatal("created infrastructure Gateway")
		}
		if kind == "Deployment" {
			deployments++
			spec := item["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
			if spec["hostNetwork"] == true {
				t.Fatal("host network enabled")
			}
		}
		if kind == "HTTPRoute" {
			spec := item["spec"].(map[string]any)
			parents := spec["parentRefs"].([]any)
			if len(parents) != 1 || parents[0].(map[string]any)["name"] != "krun-office" {
				t.Fatal("console exposed outside office gateway")
			}
		}
		if name == "krun-console" && kind != "HTTPRoute" {
			t.Fatal("renderer created a frontend proxy resource")
		}
		if kind == "Role" && strings.HasSuffix(name, "gateway-reader") {
			rules := item["rules"].([]any)
			names := rules[0].(map[string]any)["resourceNames"].([]any)
			if len(names) != 3 {
				t.Fatal("missing gateway read permissions")
			}
		}
		if kind != "NetworkPolicy" {
			continue
		}
		if !strings.HasPrefix(name, "runtime-") && !strings.HasPrefix(name, "validator-") {
			continue
		}
		spec := item["spec"].(map[string]any)
		incoming := spec["ingress"].([]any)
		expectedRules := 1
		if strings.HasPrefix(name, "runtime-") {
			expectedRules = 2
		}
		if len(incoming) != expectedRules {
			t.Fatalf("unexpected ingress for %s", name)
		}
		if expectedRules == 2 {
			peers := incoming[1].(map[string]any)["from"].([]any)
			if len(peers) != 1 {
				t.Fatalf("pool %s permits multiple gateways", name)
			}
			labels := peers[0].(map[string]any)["podSelector"].(map[string]any)["matchLabels"].(map[string]any)
			if labels["gateway.networking.k8s.io/gateway-name"] != "krun-"+strings.TrimPrefix(name, "runtime-") {
				t.Fatal("cross-zone gateway ingress")
			}
		}
	}
	if deployments != 7 {
		t.Fatalf("want control + 3 serving + 3 validators, got %d", deployments)
	}
	for _, zone := range []string{"internal", "office", "public"} {
		if seen["Service/krun-"+zone] {
			t.Fatal("created gateway Service")
		}
		for _, prefix := range []string{"runtime-", "validator-"} {
			if !seen["Deployment/"+prefix+zone] {
				t.Fatal("missing runtime")
			}
		}
	}
}

func TestAllGatewaysMustPassPreflight(t *testing.T) {
	c := multiFixture(t)
	items := []any{}
	for _, g := range c.Workers.Gateways {
		items = append(items, map[string]any{
			"metadata": map[string]any{"name": g.Name, "namespace": g.Namespace},
			"spec":     map[string]any{"listeners": []any{map[string]any{"name": "http", "protocol": "HTTP", "port": 80, "allowedRoutes": map[string]any{"namespaces": map[string]any{"from": "All"}}}}},
			"status":   map[string]any{"conditions": []any{map[string]any{"type": "Programmed", "status": "True"}}},
		})
	}
	check := func(values []any) error {
		raw, _ := json.Marshal(map[string]any{"items": values})
		return CheckGateways(c, raw)
	}
	if err := check(items); err != nil {
		t.Fatal(err)
	}
	if check(items[:2]) == nil {
		t.Fatal("missing gateway accepted")
	}
	if check([]any{items[0], items[0], items[2]}) == nil {
		t.Fatal("duplicate gateway accepted")
	}
	items[2].(map[string]any)["status"] = map[string]any{}
	if check(items) == nil {
		t.Fatal("unready third gateway accepted")
	}
	c.Deployment.ConsoleGateway = ""
	if Render(c, t.TempDir()) == nil {
		t.Fatal("ambiguous console gateway accepted")
	}
}
