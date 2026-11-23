package runtimeconfig

import (
	"encoding/json"
	"errors"

	"github.com/krun-dev/krun/internal/domain"
)

// ResourceSnapshot is used for authenticated exact-version checks and the
// compatibility route endpoint. It assembles only the requested host graph.
func ResourceSnapshot(resources []domain.RuntimeResource) (domain.EdgeConfigProjection, error) {
	byKey := map[string]domain.RuntimeResource{}
	out := domain.EdgeConfigProjection{Routes: []domain.EdgeConfigRoute{}, Versions: map[string]domain.WorkerVersion{}}
	for _, r := range resources {
		byKey[r.Key()] = r
		if r.Kind == "version" && !r.Deleted {
			var v domain.WorkerVersion
			if err := json.Unmarshal(r.Payload, &v); err != nil {
				return out, err
			}
			out.Versions[v.ID] = v
		}
	}
	for _, resource := range resources {
		if resource.Kind != "route" || resource.Deleted {
			continue
		}
		var bindings []domain.RuntimeRouteBinding
		if err := json.Unmarshal(resource.Payload, &bindings); err != nil {
			return out, err
		}
		for _, binding := range bindings {
			route := binding.EdgeConfigRoute
			scope, ok := byKey["scope:"+route.Worker]
			if !ok || scope.Deleted {
				continue
			}
			allowed := true
			for _, dependency := range scope.Dependencies {
				parent, ok := byKey[dependency.Key()]
				if !ok || parent.Deleted {
					allowed = false
				}
			}
			if !allowed {
				continue
			}
			deployment, ok := byKey["deployment:"+route.Worker+"/"+route.Environment]
			if !ok || deployment.Deleted {
				continue
			}
			var d domain.RuntimeDeployment
			if err := json.Unmarshal(deployment.Payload, &d); err != nil {
				return out, err
			}
			route.Enabled = route.Enabled && d.Enabled
			if !binding.Fixed {
				route.Version = d.Version
				route.WorkerGradualRelease = d.WorkerGradualRelease
				route.Retained = d.Retained
			}
			if route.Version != "" {
				if _, ok := out.Versions[route.Version]; !ok {
					return out, errors.New("route version dependency missing")
				}
			}
			out.Routes = append(out.Routes, route)
		}
	}
	if len(out.Routes) == 0 {
		return out, domain.NotFound()
	}
	return out, nil
}
