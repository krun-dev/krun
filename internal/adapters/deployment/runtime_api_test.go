package deployment

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestRuntimeAPIURLRoutesAndPolicies(t *testing.T) {
	for _, origin := range []string{"", "https://runtime.example.com:8443"} {
		t.Run(origin, func(t *testing.T) {
			c := multiFixture(t)
			c.Workers.ValidatorPool = "office-a"
			c.Workers.RuntimeAPIURL = origin
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
			routes := 0
			runtimePolicies := 0
			for _, item := range list.Items {
				kind := item["kind"].(string)
				name := item["metadata"].(map[string]any)["name"].(string)
				if kind == "HTTPRoute" && name == "krun-runtime-api" {
					routes++
					spec := item["spec"].(map[string]any)
					if spec["hostnames"].([]any)[0] != "runtime.example.com" {
						t.Fatal("wrong runtime hostname")
					}
					parent := spec["parentRefs"].([]any)[0].(map[string]any)
					if parent["name"] != "krun-office" {
						t.Fatal("runtime API exposed outside selected Gateway")
					}
					rules := spec["rules"].([]any)
					if len(rules) != 1 {
						t.Fatal("runtime API route includes other paths")
					}
					rule := rules[0].(map[string]any)
					path := rule["matches"].([]any)[0].(map[string]any)["path"].(map[string]any)
					backend := rule["backendRefs"].([]any)[0].(map[string]any)
					if path["value"] != "/api/internal/workers" || backend["name"] != "krun-control-internal" || backend["port"] != float64(8081) {
						t.Fatal("runtime API did not use internal listener")
					}
				}
				if kind != "NetworkPolicy" {
					continue
				}
				spec := item["spec"].(map[string]any)
				if name == "krun-control" {
					expected := 2
					if origin != "" {
						expected++
					}
					if len(spec["ingress"].([]any)) != expected {
						t.Fatal("incorrect control ingress")
					}
				}
				selector, _ := spec["podSelector"].(map[string]any)["matchLabels"].(map[string]any)
				if selector["role"] != "krun-runtime" && selector["role"] != "krun-validator" {
					continue
				}
				runtimePolicies++
				matched := false
				for _, rule := range spec["egress"].([]any) {
					entry := rule.(map[string]any)
					for _, peer := range entry["to"].([]any) {
						candidate := peer.(map[string]any)
						pods, ok := candidate["podSelector"].(map[string]any)
						if !ok {
							continue
						}
						labels := pods["matchLabels"].(map[string]any)
						if labels["gateway.networking.k8s.io/gateway-name"] != "krun-office" {
							continue
						}
						for _, port := range entry["ports"].([]any) {
							if port.(map[string]any)["port"] == float64(8443) {
								matched = true
							}
						}
					}
				}
				if matched != (origin != "") {
					t.Fatalf("wrong runtime egress for %s", name)
				}
			}
			expectedRoutes := 0
			if origin != "" {
				expectedRoutes = 1
			}
			if routes != expectedRoutes || runtimePolicies != 4 {
				t.Fatal("incorrect runtime API topology")
			}
		})
	}
}

func TestMergedDeploymentRoutesRuntimeAPIToMainService(t *testing.T) {
	c := multiFixture(t)
	c.MergedDeployment = true
	c.Workers.RuntimeAPIURL = "https://runtime.example.com"
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
	found := false
	for _, item := range list.Items {
		name := item["metadata"].(map[string]any)["name"]
		if item["kind"] == "HTTPRoute" && name == "krun-runtime-api" {
			spec := item["spec"].(map[string]any)
			backend := spec["rules"].([]any)[0].(map[string]any)["backendRefs"].([]any)[0].(map[string]any)
			if backend["name"] != "krun-control" || backend["port"] != float64(8080) {
				t.Fatal("merged Runtime API did not use main service")
			}
			found = true
		}
		if item["kind"] == "NetworkPolicy" && name == "krun-control" {
			if len(item["spec"].(map[string]any)["ingress"].([]any)) != 2 {
				t.Fatal("merged mode unnecessarily exposed the internal listener to Gateway")
			}
		}
	}
	if !found {
		t.Fatal("runtime API route missing")
	}
}
