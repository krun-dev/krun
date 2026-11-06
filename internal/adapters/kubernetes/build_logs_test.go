package kubernetes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBuildLogReadsRunningPodContainers(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/pods") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"items":[{"metadata":{"name":"running-builder"}}]}`))
			return
		}
		if r.URL.Query().Get("limitBytes") != "16384" {
			t.Error("missing output bound")
		}
		switch r.URL.Query().Get("container") {
		case "builder-build":
			_, _ = w.Write([]byte("building now\n"))
		case "builder-pack":
			http.Error(w, "container not started", 400)
		default:
			t.Errorf("unexpected container %s", r.URL.RawQuery)
		}
	}))
	defer server.Close()
	client := &Client{Base: server.URL, Client: server.Client(), Namespace: "test"}
	if got := client.BuildLog(context.Background(), "build-job"); got != "building now\n" {
		t.Fatalf("running logs: %q", got)
	}
}
