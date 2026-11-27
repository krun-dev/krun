package kubernetes

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/krun-dev/krun/internal/config"
)

func TestHostnameRouteUsesSpecificBackendAndProtectsNewerRevision(t *testing.T) {
	requests := make(chan map[string]any, 8)
	var current map[string]any
	kube := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			if current == nil {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(current)
		case http.MethodPost, http.MethodPatch:
			var object map[string]any
			if err := json.NewDecoder(r.Body).Decode(&object); err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			requests <- object
			current = map[string]any{
				"metadata": map[string]any{
					"resourceVersion": "57",
					"annotations":     object["metadata"].(map[string]any)["annotations"],
				},
				"spec": object["spec"],
			}
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected method: %s", r.Method)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer kube.Close()

	client := Client{Base: kube.URL, Namespace: "workers", Client: kube.Client(), requestTimeoutSeconds: 300}
	gateway := config.WorkerGateway{Name: "web", Namespace: "gateway", Section: "https"}
	rules := []HostnameRule{
		{Match: "prefix", Path: "/", Service: "shop", Namespace: "static", Port: 80, HostHeader: "shop.origin.example.com"},
		{Match: "prefix", Path: "/api", Service: "runtime", Port: 8081, Worker: "worker-orders"},
		{Match: "exact", Path: "/legacy", Service: "legacy", Port: 8080},
		{Match: "prefix", Path: "/paused", Status: 410},
	}
	ctx := t.Context()
	host := "shop.apps.example.com"
	if err := client.EnsureHostnameRoute(ctx, "hostname-shop", host, 4, gateway, rules); err != nil {
		t.Fatal(err)
	}
	obj := <-requests
	spec := obj["spec"].(map[string]any)
	if got := spec["hostnames"].([]any)[0]; got != host {
		t.Fatalf("route host = %v; want %s", got, host)
	}
	routeEntries := spec["rules"].([]any)
	if got := routeEntries[1].(map[string]any)["timeouts"].(map[string]any)["request"]; got != "300s" {
		t.Fatalf("Worker timeout = %v", got)
	}
	wire, err := json.Marshal(spec["rules"])
	if err != nil {
		t.Fatal(err)
	}
	var entries []struct {
		Matches []struct {
			Path struct{ Type, Value string }
		}
		BackendRefs []struct {
			Name, Namespace string
			Port            int
		}
		Filters []struct {
			Type           string
			URLRewrite     struct{ Hostname string }
			ExtensionRef   struct{ Group, Kind, Name string }
			HeaderModifier struct {
				Set []struct{ Name, Value string }
			} `json:"requestHeaderModifier"`
		}
	}
	if err := json.Unmarshal(wire, &entries); err != nil {
		t.Fatal(err)
	}
	if entries[0].BackendRefs[0].Namespace != "static" || entries[0].Filters[1].URLRewrite.Hostname != rules[0].HostHeader {
		t.Fatalf("static origin lost Service namespace or Host rewrite: %+v", entries[0])
	}
	marker := entries[1].Filters[0].HeaderModifier.Set[0]
	if entries[1].BackendRefs[0].Name != "runtime" || marker.Name != "x-krun-route" || marker.Value != "worker-orders:prefix:/api" {
		t.Fatalf("Worker route lost its expected target: %+v", entries[1])
	}
	if entries[2].Matches[0].Path.Type != "Exact" || entries[2].BackendRefs[0].Port != 8080 {
		t.Fatalf("HTTP service route changed match or backend: %+v", entries[2])
	}
	if entries[3].Filters[0].ExtensionRef.Name != "krun-hostname-410" || len(entries[3].BackendRefs) != 0 {
		t.Fatalf("disabled path could fall through to the root: %+v", entries[3])
	}

	for _, revision := range []int{3, 4, 5} {
		if err := client.EnsureHostnameRoute(ctx, "hostname-shop", host, revision, gateway, rules); err != nil {
			t.Fatal(err)
		}
	}
	if len(requests) != 1 {
		t.Fatalf("expected only the newer revision to issue a patch; got %d writes", len(requests))
	}
	patch := <-requests
	if got := patch["metadata"].(map[string]any)["resourceVersion"]; got != "57" {
		t.Fatalf("patch lacked a resourceVersion precondition: %+v", patch["metadata"])
	}
}
