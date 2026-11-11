package control

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestWorkerMetricsDashboard(t *testing.T) {
	s, _, base := workerTestServer(t)

	r := request(s, http.MethodGet, base+"/metrics", "krun_cli_viewer", "")
	if r.Code != http.StatusOK || strings.TrimSpace(r.Body.String()) != "{}" {
		t.Fatalf("unconfigured metrics: %d %s", r.Code, r.Body.String())
	}

	if err := json.Unmarshal([]byte(`{"worker_metrics":{"grafana_url":"https://charts.example.com/grafana/","dashboard_uid":"custom-dashboard"}}`), &s.Config); err != nil {
		t.Fatal(err)
	}
	r = request(s, http.MethodGet, base+"/metrics", "krun_cli_viewer", "")
	var body struct {
		DashboardURL string `json:"dashboard_url"`
	}
	if r.Code != http.StatusOK || json.Unmarshal(r.Body.Bytes(), &body) != nil {
		t.Fatalf("metrics: %d %s", r.Code, r.Body.String())
	}
	dashboard, err := url.Parse(body.DashboardURL)
	if err != nil || dashboard.Host != "charts.example.com" || dashboard.Path != "/grafana/d/custom-dashboard" ||
		dashboard.Query().Get("var-organization") != "org-a" || dashboard.Query().Get("var-worker") != "svc-a" {
		t.Fatalf("unexpected dashboard: %q (%v)", body.DashboardURL, err)
	}
	if !strings.Contains(s.consoleContentSecurityPolicy(), "frame-src 'self' https://charts.example.com;") {
		t.Fatal(s.consoleContentSecurityPolicy())
	}

	r = request(s, http.MethodGet, "/api/v1/orgs/org-b/workers/svc-b/metrics", "krun_cli_viewer", "")
	if r.Code != http.StatusForbidden && r.Code != http.StatusNotFound {
		t.Fatalf("cross-organization request: %d %s", r.Code, r.Body.String())
	}
}
