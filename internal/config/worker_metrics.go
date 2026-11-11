package config

import (
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
)

const DefaultWorkerMetricsDashboard = "krun-worker-metrics"

type WorkerMetricsConfig struct {
	GrafanaURL   string `json:"grafana_url"`
	DashboardUID string `json:"dashboard_uid"`
}

func (c *WorkerMetricsConfig) validate(development bool) error {
	if c == nil {
		return nil
	}
	c.GrafanaURL = strings.TrimRight(strings.TrimSpace(c.GrafanaURL), "/")
	u, err := url.Parse(c.GrafanaURL)
	if err != nil || u.Hostname() == "" || u.User != nil ||
		strings.ContainsAny(u.Host, `*'";`) || strings.ContainsAny(c.GrafanaURL, "?#") {
		return errors.New("worker_metrics.grafana_url must be an absolute URL without credentials, query or fragment")
	}
	if u.Port() != "" {
		port, err := strconv.Atoi(u.Port())
		if err != nil || port < 1 || port > 65535 {
			return errors.New("worker_metrics.grafana_url has an invalid port")
		}
	}
	loopback := strings.EqualFold(u.Hostname(), "localhost") || net.ParseIP(u.Hostname()).IsLoopback()
	if u.Scheme != "https" && !(development && u.Scheme == "http" && loopback) {
		return errors.New("worker_metrics.grafana_url requires HTTPS or development loopback HTTP")
	}
	if c.DashboardUID == "" {
		c.DashboardUID = DefaultWorkerMetricsDashboard
	}
	if len(c.DashboardUID) > 40 || !Slug.MatchString(c.DashboardUID) {
		return errors.New("worker_metrics.dashboard_uid must be a Grafana dashboard UID")
	}
	return nil
}
