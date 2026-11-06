package deployment

import "strconv"

func (r *renderer) runtimeAPIPort() int {
	origin, _ := r.cfg.Workers.RuntimeAPIOrigin()
	if origin == nil {
		return 0
	}
	if origin.Port() == "" {
		return 443
	}
	port, _ := strconv.Atoi(origin.Port())
	return port
}

// The existing selected Gateway serves the configured origin. DNS,
// certificates and the edge's HTTPS forwarding remain operator-managed.
func (r *renderer) runtimeAPIRoute() {
	origin, _ := r.cfg.Workers.RuntimeAPIOrigin()
	if origin == nil {
		return
	}
	r.add("gateway.networking.k8s.io/v1", "HTTPRoute", "krun-runtime-api", map[string]any{
		"spec": map[string]any{
			"parentRefs": []any{map[string]string{
				"name":        r.gateway.Name,
				"namespace":   r.gateway.Namespace,
				"sectionName": r.gateway.Section,
			}},
			"hostnames": []string{origin.Hostname()},
			"rules": []any{map[string]any{
				"matches": []any{map[string]any{
					"path": map[string]string{"type": "PathPrefix", "value": "/api/internal/workers"},
				}},
				"backendRefs": []any{map[string]any{
					"name": "krun-control-internal",
					"port": integerPort(r.cfg.InternalListen),
				}},
			}},
		},
	})
}
