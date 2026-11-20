package kubernetes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCleanupBuildResourcesScopesAndUIDPreconditions(t *testing.T) {
	var deleted []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/jobs") {
			if r.URL.Query().Get("labelSelector") != "app=krun-builder,krun.dev/organization-id=org-a,krun.dev/worker-id=worker-a" {
				t.Errorf("selector %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"metadata":{},"items":[{"metadata":{"name":"job-a","uid":"job-uid","labels":{"app":"krun-builder","krun.dev/organization-id":"org-a","krun.dev/worker-id":"worker-a"}}}]}`))
			return
		}
		if r.Method == http.MethodGet && (strings.HasSuffix(r.URL.Path, "/pods") || strings.HasSuffix(r.URL.Path, "/secrets")) {
			_, _ = w.Write([]byte(`{"metadata":{},"items":[]}`))
			return
		}
		if r.Method == http.MethodDelete {
			var options struct {
				Preconditions struct {
					UID string `json:"uid"`
				} `json:"preconditions"`
			}
			if err := json.NewDecoder(r.Body).Decode(&options); err != nil {
				t.Error(err)
			}
			if options.Preconditions.UID != "job-uid" {
				t.Errorf("missing UID precondition: %+v", options)
			}
			deleted = append(deleted, r.URL.Path)
			w.WriteHeader(200)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	client := &Client{Base: server.URL, Namespace: "test", Client: server.Client()}
	count, err := client.CleanupBuildResources(context.Background(), "org-a", "worker-a", nil)
	if err != nil || count != 1 || len(deleted) != 1 || !strings.HasSuffix(deleted[0], "/jobs/job-a") {
		t.Fatalf("count=%d deleted=%v err=%v", count, deleted, err)
	}
	if _, err := client.CleanupBuildResources(context.Background(), "org-a", "worker-a,app!=krun-builder", nil); err == nil {
		t.Fatal("accepted invalid selector input")
	}
}

func TestCleanupRejectsForeignLegacyObject(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/legacy") {
			_, _ = w.Write([]byte(`{"metadata":{"name":"legacy","uid":"uid","labels":{"app":"krun-builder","krun.dev/worker-id":"someone-else"}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"metadata":{},"items":[]}`))
	}))
	defer server.Close()
	client := &Client{Base: server.URL, Namespace: "test", Client: server.Client()}
	if _, err := client.CleanupBuildResources(context.Background(), "org-a", "worker-a", []string{"legacy"}); err == nil {
		t.Fatal("accepted resource belonging to another Worker")
	}
}

func TestCleanupFindsLegacySecretWithoutJob(t *testing.T) {
	deleted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/secrets/old-job") && !deleted {
			_, _ = w.Write([]byte(`{"metadata":{"name":"old-job","uid":"legacy-secret","labels":{"app":"krun-builder"}}}`))
			return
		}
		if r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/secrets/old-job") {
			deleted = true
			w.WriteHeader(http.StatusOK)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/jobs/old-job") || strings.HasSuffix(r.URL.Path, "/secrets/old-job") {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"metadata":{},"items":[]}`))
	}))
	defer server.Close()
	client := &Client{Base: server.URL, Namespace: "test", Client: server.Client()}
	count, err := client.CleanupBuildResources(context.Background(), "org-a", "worker-a", []string{"old-job"})
	if err != nil || count != 1 || !deleted {
		t.Fatalf("count=%d deleted=%v error=%v", count, deleted, err)
	}
	count, err = client.CleanupBuildResources(context.Background(), "org-a", "worker-a", []string{"old-job"})
	if err != nil || count != 0 {
		t.Fatalf("repeat count=%d error=%v", count, err)
	}
}
