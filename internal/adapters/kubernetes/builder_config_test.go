package kubernetes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/domain"
)

func TestBuildJobUsesConfiguredResourcesAndScheduling(t *testing.T) {
	for _, repository := range []bool{false, true} {
		t.Run(map[bool]string{false: "direct", true: "repository"}[repository], func(t *testing.T) {
			var job map[string]any
			var secret map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/jobs/"):
					http.NotFound(w, r)
				case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/secrets"):
					if err := json.NewDecoder(r.Body).Decode(&secret); err != nil {
						t.Error(err)
					}
					w.WriteHeader(http.StatusCreated)
				case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/jobs"):
					if err := json.NewDecoder(r.Body).Decode(&job); err != nil {
						t.Error(err)
					}
					w.WriteHeader(http.StatusCreated)
					_, _ = w.Write([]byte(`{"metadata":{"uid":"id"}}`))
				default:
					w.WriteHeader(http.StatusOK)
				}
			}))
			defer server.Close()
			k := &Client{Base: server.URL, Namespace: "test", Client: server.Client(), builderBuildResources: config.BuilderResources{Limits: config.BuilderResourceValues{CPU: "4", Memory: "8Gi"}}, builderPackResources: config.BuilderResources{Limits: config.BuilderResourceValues{Memory: "1Gi"}}, builderNodeSelector: map[string]string{"workload": "build"}, builderTolerations: []config.BuilderToleration{{Key: "workload", Operator: "Equal", Value: "build", Effect: "NoSchedule"}}}
			b := domain.WorkerBuild{Job: "build", Worker: "worker-id", Organization: "org-id", Entrypoint: "index.js"}
			if repository {
				b.Repository = &domain.WorkerBuildRepository{ConfigPath: "wrangler.jsonc"}
			}
			if err := k.EnsureBuildJob(context.Background(), b, domain.WorkerBuildPrivate{}, "https://source", "https://upload", "builder:test", "Never"); err != nil {
				t.Fatal(err)
			}
			for _, metadata := range []map[string]any{
				job["metadata"].(map[string]any),
				secret["metadata"].(map[string]any),
				job["spec"].(map[string]any)["template"].(map[string]any)["metadata"].(map[string]any),
			} {
				labels := metadata["labels"].(map[string]any)
				if labels["krun.dev/organization-id"] != "org-id" || labels["krun.dev/worker-id"] != "worker-id" {
					t.Fatalf("wrong ownership labels: %+v", labels)
				}
			}
			pod := job["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
			if pod["nodeSelector"].(map[string]any)["workload"] != "build" || pod["tolerations"].([]any)[0].(map[string]any)["effect"] != "NoSchedule" {
				t.Fatalf("scheduling: %#v", pod)
			}
			build := pod["containers"].([]any)[0].(map[string]any)
			if repository {
				packLimits := build["resources"].(map[string]any)["limits"].(map[string]any)
				if packLimits["memory"] != "1Gi" {
					t.Fatalf("pack limits: %#v", packLimits)
				}
				build = pod["initContainers"].([]any)[0].(map[string]any)
			}
			resources := build["resources"].(map[string]any)
			limits := resources["limits"].(map[string]any)
			requests := resources["requests"].(map[string]any)
			if limits["cpu"] != "4" || limits["memory"] != "8Gi" || requests["cpu"] != "100m" {
				t.Fatalf("build resources: %#v", resources)
			}
		})
	}
}
