package control

import (
	"net/http"
	"net/url"
)

func (s *Server) workerMetrics(st *State, p Principal, r *http.Request) (any, int, error) {
	target, err := s.workerApplication(r).WorkerCollectionTarget(st, workerRef(r), "worker.read", p)
	if err != nil {
		return nil, 0, err
	}
	var out struct {
		DashboardURL string `json:"dashboard_url,omitempty"`
	}
	if config := s.Config.WorkerMetrics; config != nil {
		base, err := url.Parse(config.GrafanaURL)
		if err != nil {
			return nil, 0, err
		}
		dashboard := base.JoinPath("d", config.DashboardUID)
		dashboard.RawQuery = url.Values{
			"var-organization": {target.Organization},
			"var-worker":       {target.ID},
		}.Encode()
		out.DashboardURL = dashboard.String()
	}
	return out, http.StatusOK, nil
}
