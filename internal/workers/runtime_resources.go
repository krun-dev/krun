package workers

import (
	"context"

	"github.com/krun-dev/krun/internal/domain"
)

// PublishRuntimeResources materializes only stable hostname bindings. Version
// switches read the deployment row directly and never rewrite hostname rows.
func (w *Controller) PublishRuntimeResources(ctx context.Context) (int64, error) {
	dirty, err := w.Service.Store.RuntimeRouteDirty(ctx, 128)
	if err != nil {
		return 0, err
	}
	for _, item := range dirty {
		state, err := w.Service.Store.EdgeConfigState(ctx, []string{item.ID})
		if err != nil {
			return 0, err
		}
		projection := EdgeConfigFromState(state, w.Config)
		rules := map[string]bool{}
		for _, host := range state.Workers.ManagedHostnames {
			for _, rule := range host.Routes {
				rules[host.Host+"\x00"+rule.Match+":"+rule.Path] = rule.Enabled
			}
		}
		bindings := make([]domain.RuntimeRouteBinding, 0, len(projection.Routes))
		for _, route := range projection.Routes {
			fixed := route.Expires != nil
			if fixed {
				if route.ScriptName == "" {
					route.ScriptName = state.Resources[route.Worker].Locator
				}
				if version, ok := projection.Versions[route.Version]; ok {
					route.Environment = version.Environment
				}
			} else {
				if route.Environment == "" {
					route.Environment = "production"
				}
				route.Version = ""
			}
			route.Candidate = ""
			route.CandidateWeight = 0
			route.Retained = nil
			route.Enabled = true
			if route.Match != "" {
				route.Enabled = rules[route.Host+"\x00"+route.Key()]
			}
			bindings = append(bindings, domain.RuntimeRouteBinding{EdgeConfigRoute: route, Fixed: fixed})
		}
		if _, err := w.Service.Store.PublishRuntimeBindings(ctx, item, bindings); err != nil {
			return 0, err
		}
	}
	if err := w.Service.Store.DispatchRuntimeNotifications(ctx, w.Replica); err != nil {
		return 0, err
	}
	return int64(len(dirty)), w.Service.Store.CleanupRuntimeResources(ctx)
}
