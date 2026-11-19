package cli

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWorkerMetricsCommand(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.EscapedPath() != "/api/v1/orgs/team/workers/worker-a/metrics" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.EscapedPath())
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"dashboard_url":"https://grafana.example.com/d/example"}`)
	}))
	defer server.Close()

	client := Client{Profile: Profile{Endpoint: server.URL}, Out: io.Discard, Err: io.Discard}
	if err := client.workers(t.Context(), []string{"metrics", "--org", "team", "--worker", "worker-a"}); err != nil {
		t.Fatal(err)
	}
}
