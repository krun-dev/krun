package workers

import (
	"context"
	"errors"
	"fmt"

	"github.com/krun-dev/krun/internal/adapters/kubernetes"
	appconfig "github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/securetoken"
)

func (w *Controller) reconcileManagedHostnames(ctx context.Context, desired map[string]bool) error {
	if w.Service == nil || w.Service.Store == nil {
		return nil
	}
	items, err := w.Service.Store.GatewayManagedHostnames(ctx)
	if err != nil || len(items) == 0 {
		return err
	}
	var failures []error
	responsesReady := false
	for _, item := range items {
		suffix, ok := w.Config.Domain(item.SuffixID)
		if !ok {
			continue
		}
		matched, ok := w.Config.DomainForHost(item.Host)
		if !ok || matched.ID != suffix.ID || !matched.Allows(appconfig.WorkerDomainType(item.Type)) || w.Config.ManagedHostReserved(item.Host) {
			continue
		}
		gateway, ok := w.Config.Gateway(suffix.Gateway)
		if !ok {
			continue
		}
		if item.Type == string(appconfig.WorkerDomainCustom) {
			pool, err := w.Config.DomainRuntimePool(suffix.ID)
			if err != nil {
				failures = append(failures, err)
				continue
			}
			name := "workers-preview-" + securetoken.Digest(item.Host)[:24]
			desired[name] = true
			if _, err = w.Kube.EnsureRoute(ctx, name, pool.RuntimeService, gateway, []string{"*." + item.Host}, pool); err != nil {
				failures = append(failures, fmt.Errorf("preview hostname %s: %w", item.Host, err))
			}
			continue
		}
		if !responsesReady {
			if err = w.Kube.EnsureHostnameResponses(ctx); err != nil {
				return err
			}
			responsesReady = true
		}
		name := "workers-hostname-" + securetoken.Digest(item.Host)[:24]
		desired[name] = true
		if err := w.Kube.EnsureHostnameRoute(ctx, name, item.Host, item.Revision, gateway, w.gatewayHostnameRules(item), desired); err != nil {
			failures = append(failures, fmt.Errorf("hostname %s: %w", item.Host, err))
		}
	}
	return errors.Join(failures...)
}

func (w *Controller) gatewayHostnameRules(item domain.ManagedHostname) []kubernetes.HostnameRule {
	pool, poolErr := w.Config.DomainRuntimePool(item.SuffixID)
	out := make([]kubernetes.HostnameRule, 0, len(item.Routes)+1)
	root := false
	for _, route := range item.Routes {
		rule := kubernetes.HostnameRule{Match: route.Match, Path: route.Path}
		switch {
		case !route.Enabled:
			rule.Status = 410
		case route.Origin != "":
			origin, ok := w.Config.ManagedOriginFor(item.Organization, item.SuffixID, route.Origin)
			if ok {
				rule.Service, rule.Namespace, rule.Port = origin.Service, origin.Namespace, origin.Port
				rule.HostHeader = origin.HostHeader
			} else {
				rule.Status = 503
			}
		case poolErr != nil:
			rule.Status = 503
		default:
			rule.Service, rule.Port, rule.Worker = pool.RuntimeService, 8081, route.Worker
			if pool.Execution.Streams != nil {
				rule.StreamService = pool.StreamService()
				rule.SSEPaths = pool.Execution.Streams.SSEPaths
			}
		}
		out = append(out, rule)
		root = root || (route.Match == "prefix" && route.Path == "/")
	}
	if !root {
		out = append(out, kubernetes.HostnameRule{Match: "prefix", Path: "/", Status: 404})
	}
	return out
}
