package kubernetes

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/krun-dev/krun/internal/config"
)

func TestExistingWorkerRouteReceivesRequestTimeout(t *testing.T) {
	var patch map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"spec": map[string]any{
				"parentRefs": []any{map[string]any{"name": "gateway", "namespace": "gateway-system", "sectionName": "http"}},
				"hostnames":  []string{"worker.example.com"},
				"rules": []any{map[string]any{
					"backendRefs": []any{map[string]any{"name": "runtime", "port": 8081}},
					"filters":     []any{map[string]any{"requestHeaderModifier": map[string]any{"remove": []string{"x-krun-route"}}}},
				}},
			}})
		case http.MethodPatch:
			if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
				t.Error(err)
			}
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	}))
	defer server.Close()

	client := Client{Base: server.URL, Namespace: "workers", Client: server.Client(), requestTimeoutSeconds: 300}
	gateway := config.WorkerGateway{Name: "gateway", Namespace: "gateway-system", Section: "http"}
	if _, err := client.EnsureRoute(t.Context(), "worker", "runtime", gateway, []string{"worker.example.com"}); err != nil {
		t.Fatal(err)
	}
	if patch == nil {
		t.Fatal("existing route without timeout was not patched")
	}
	rules := patch["spec"].(map[string]any)["rules"].([]any)
	timeouts := rules[0].(map[string]any)["timeouts"].(map[string]any)
	if timeouts["request"] != "300s" {
		t.Fatalf("unexpected timeout: %v", timeouts)
	}
}
