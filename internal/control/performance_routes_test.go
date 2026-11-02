package control

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	kubernetes "github.com/krun-dev/krun/internal/adapters/kubernetes"
	appconfig "github.com/krun-dev/krun/internal/config"
	workerapp "github.com/krun-dev/krun/internal/workers"
)

func TestWorkerRouteInventoryPaginationAndReuse(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("limit") != "100" {
			t.Error("route inventory is not paginated")
		}
		names := []any{}
		start, end, token := 0, 100, "next-page"
		if r.URL.Query().Get("continue") == "next-page" {
			start, end, token = 100, 103, ""
		}
		for i := start; i < end; i++ {
			names = append(names, map[string]any{"metadata": map[string]string{"name": fmt.Sprintf("workers-%03d", i)}})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"metadata": map[string]string{"continue": token}, "items": names})
	}))
	defer server.Close()
	kube := &kubernetes.Client{Base: server.URL, Client: server.Client(), Namespace: "test"}
	names, err := kube.ListRoutes(context.Background())
	if err != nil || len(names) != 103 || calls != 2 {
		t.Fatalf("names=%d calls=%d err=%v", len(names), calls, err)
	}
	ctx := context.WithValue(context.Background(), kubernetes.WorkerRouteInventoryKey{}, names)
	for i := 0; i < 1000; i++ {
		if _, err = kubernetes.StaleWorkerRoutes(ctx, kube, "workers-", map[string]bool{}); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 2 {
		t.Fatalf("per-service stale checks repeated namespace LIST %d times", calls)
	}
}

func TestSharedGatewayRouteCountIndependentOfSites(t *testing.T) {
	posts := []map[string]any{}
	lists := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/httproutes"):
			lists++
			_, _ = w.Write([]byte(`{"items":[]}`))
		case r.Method == "POST":
			var route map[string]any
			_ = json.NewDecoder(r.Body).Decode(&route)
			posts = append(posts, route)
			w.WriteHeader(201)
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	config := workerProjectionConfig([]appconfig.WorkerDomain{{ID: "internal", Domain: "workers.test", Gateway: "edge"}})
	config.Gateways = []appconfig.WorkerGateway{{ID: "edge", Name: "edge", Namespace: "test", Section: "http"}}

	controller := &workerapp.Controller{Config: config, Kube: &kubernetes.Client{Base: server.URL, Client: server.Client(), Namespace: "test"}}
	_, err := controller.ReconcileRoutes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(posts) != 1 || lists != 1 {
		t.Fatalf("shared routing produced %d routes and %d namespace lists", len(posts), lists)
	}
	hosts := posts[0]["spec"].(map[string]any)["hostnames"].([]any)
	if hosts[0] != "*."+config.PoolHostSuffix("default", "workers.test") {
		t.Fatalf("unpartitioned shared hostname: %v", hosts)
	}
}
