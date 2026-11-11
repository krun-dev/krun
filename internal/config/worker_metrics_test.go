package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadWorkerMetricsConfig(t *testing.T) {
	tests := []struct {
		name, grafanaURL, dashboardUID string
		wantError                      bool
	}{
		{name: "HTTPS with a subpath", grafanaURL: "https://grafana.example.com/monitoring/"},
		{name: "development loopback", grafanaURL: "http://127.0.0.1:3000", dashboardUID: "custom-worker-metrics"},
		{name: "remote HTTP", grafanaURL: "http://grafana.example.com", wantError: true},
		{name: "credentials", grafanaURL: "https://user@grafana.example.com", wantError: true},
		{name: "wildcard", grafanaURL: "https://*.example.com", wantError: true},
		{name: "query", grafanaURL: "https://grafana.example.com/?kiosk=1", wantError: true},
		{name: "port out of range", grafanaURL: "https://grafana.example.com:65536", wantError: true},
		{name: "invalid dashboard UID", grafanaURL: "https://grafana.example.com", dashboardUID: "../other", wantError: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			const base = "development: true\npublic_url: http://127.0.0.1:8080\ndb: postgres://localhost/krun_test\n"
			path := filepath.Join(t.TempDir(), "dev.yaml")
			raw := base + "worker_metrics:\n  grafana_url: " + tc.grafanaURL + "\n"
			if tc.dashboardUID != "" {
				raw += "  dashboard_uid: " + tc.dashboardUID + "\n"
			}
			if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadConfig(path)
			if (err != nil) != tc.wantError {
				t.Fatalf("LoadConfig error = %v, want error: %t", err, tc.wantError)
			}
			if tc.wantError {
				return
			}
			wantUID := tc.dashboardUID
			if wantUID == "" {
				wantUID = "krun-worker-metrics"
			}
			if cfg.WorkerMetrics == nil || cfg.WorkerMetrics.DashboardUID != wantUID {
				t.Fatalf("worker metrics config = %+v, want dashboard UID %q", cfg.WorkerMetrics, wantUID)
			}
		})
	}
}
