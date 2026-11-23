package runtimebundle

import (
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/domain"
	yaml "go.yaml.in/yaml/v3"
)

func TestRenderPoolManifests(t *testing.T) {
	zero := 0
	seconds := int64(60)
	ndots := "2"
	const secretValue = "fixture-value-kept-out-of-manifests"
	pool := config.WorkerRuntimePool{
		ID:              "office-a",
		RuntimeService:  "runtime-office",
		RuntimeToken:    secretValue,
		ManagementToken: secretValue,
		Execution: config.WorkerExecutionPool{
			ComputeProcesses: &zero,
			Streams:          &config.WorkerStreamAdmission{SSEPaths: []string{"/events"}},
		},
		Deployment: &config.RuntimePoolDeployment{
			Image:        "registry.example.com/runtime:v1",
			Replicas:     3,
			PodLabels:    map[string]string{"workload.example.com/tier": "serving"},
			NodeSelector: map[string]string{"workload": "serving"},
			Tolerations: []config.BuilderToleration{{
				Key: "workload", Operator: "Equal", Value: "serving", Effect: "NoExecute", TolerationSeconds: &seconds,
			}},
			DNSPolicy: "None",
			DNSConfig: &config.RuntimePodDNSConfig{
				Nameservers: []string{"192.0.2.53"},
				Searches:    []string{"workers.svc.cluster.local"},
				Options:     []config.RuntimePodDNSOption{{Name: "ndots", Value: &ndots}, {Name: "edns0"}},
			},
			SecretName:         "runtime-office-credentials",
			PeerService:        "runtime-office",
			PodUIDAnnotation:   "example.com/pod-uid",
			StorageEgressCIDRs: []string{"192.0.2.0/24"},
		},
	}
	cfg := config.Config{
		InternalListen: "0.0.0.0:8087",
		Workers: &config.WorkersConfig{
			Namespace:    "workers",
			RuntimePools: []config.WorkerRuntimePool{pool},
			ArtifactStorage: config.WorkerArtifactStorageConfig{
				ConfigBaseURL: "https://artifacts.example.com/config",
				Credentials:   config.ArtifactStorageCredentials{Mode: "ak_sk", SecretAccessKey: secretValue},
			},
		},
	}

	render := func() domain.RuntimePoolManifests {
		t.Helper()
		result, err := RenderPoolManifests(cfg, pool.ID)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	rendered := render()
	if again := render(); again != rendered {
		t.Fatal("manifests must be deterministic")
	}
	if strings.Contains(rendered.YAML(), secretValue) {
		t.Fatal("render exposed a configured credential")
	}

	documents := parsePoolManifests(t, rendered.YAML())
	kinds := make([]string, 0, len(documents))
	for _, document := range documents {
		kinds = append(kinds, document["kind"].(string))
	}
	wantKinds := []string{"ConfigMap", "Deployment", "Service", "BackendConfigPolicy", "Service", "BackendConfigPolicy"}
	if !reflect.DeepEqual(kinds, wantKinds) {
		t.Fatalf("kinds = %v, want %v", kinds, wantKinds)
	}
	name := documents[0]["metadata"].(map[string]any)["name"].(string)
	if documents[0]["immutable"] != true || !strings.HasPrefix(name, "runtime-office-config-") {
		t.Fatal("expected a content-addressed ConfigMap")
	}
	template := documents[1]["spec"].(map[string]any)["template"].(map[string]any)
	labels := template["metadata"].(map[string]any)["labels"].(map[string]any)
	if labels["app"] != pool.RuntimeService || labels["workload.example.com/tier"] != "serving" {
		t.Fatalf("unexpected Pod labels: %v", labels)
	}
	pod := template["spec"].(map[string]any)
	if pod["dnsPolicy"] != "None" || pod["automountServiceAccountToken"] != false ||
		pod["nodeSelector"].(map[string]any)["workload"] != "serving" {
		t.Fatalf("unexpected Pod settings: %v", pod)
	}
	dns := pod["dnsConfig"].(map[string]any)
	if !reflect.DeepEqual(dns["nameservers"], []any{"192.0.2.53"}) ||
		!reflect.DeepEqual(dns["searches"], []any{"workers.svc.cluster.local"}) ||
		!reflect.DeepEqual(dns["options"], []any{map[string]any{"name": "ndots", "value": "2"}, map[string]any{"name": "edns0"}}) {
		t.Fatalf("unexpected dnsConfig: %v", dns)
	}
	if pod["tolerations"].([]any)[0].(map[string]any)["tolerationSeconds"] != 60 {
		t.Fatal("tolerations were not propagated")
	}
	for _, part := range []string{name, "runtime-office-credentials", "artifact-access", "example.com/pod-uid", "POOL_PEER_DNS"} {
		if !strings.Contains(rendered.Deployment, part) {
			t.Errorf("Deployment missing %q", part)
		}
	}
	serviceSpec := documents[2]["spec"].(map[string]any)
	if serviceSpec["clusterIP"] != "None" || len(serviceSpec["ports"].([]any)) != 3 {
		t.Fatalf("unexpected joint serving and peer Service: %v", serviceSpec)
	}

	cfg.Workers.ArtifactStorage.Credentials.Mode = "sts"
	sts := render()
	if strings.Contains(sts.Deployment, name) || strings.Contains(sts.Deployment, "artifact-access") ||
		strings.Contains(sts.ConfigMap, "STORAGE_SECRET_KEY") || !strings.Contains(sts.ConfigMap, "CONFIG_STORAGE_STS") {
		t.Fatal("changing Cap'n Proto must roll the ConfigMap reference and remove the static storage mount")
	}
}

func parsePoolManifests(t *testing.T, manifests string) []map[string]any {
	t.Helper()
	decoder := yaml.NewDecoder(strings.NewReader(manifests))
	var documents []map[string]any
	for {
		var manifest map[string]any
		err := decoder.Decode(&manifest)
		if err == io.EOF {
			return documents
		}
		if err != nil {
			t.Fatal(err)
		}
		documents = append(documents, manifest)
	}
}
