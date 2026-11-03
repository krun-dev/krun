package kubernetes

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strings"

	appconfig "github.com/krun-dev/krun/internal/config"
)

const (
	WorkerBuilderWorkSize         = "2Gi"
	WorkerBuilderTmpSize          = "256Mi"
	WorkerBuilderEphemeralRequest = "512Mi"
	WorkerBuilderEphemeralLimit   = "3Gi"
)

// listRoutes returns the names of HTTPRoutes in the workers namespace.
type WorkerRouteInventoryKey struct{}

func (k *Client) ListRoutes(ctx context.Context) ([]string, error) {
	if names, ok := ctx.Value(WorkerRouteInventoryKey{}).([]string); ok {
		return names, nil
	}
	base := "/apis/gateway.networking.k8s.io/v1/namespaces/" + k.Namespace + "/httproutes"
	out := []string{}
	continuation := ""
	for {
		var list struct {
			Metadata struct {
				Continue string `json:"continue"`
			} `json:"metadata"`
			Items []struct {
				Metadata struct {
					Name string `json:"name"`
				} `json:"metadata"`
			} `json:"items"`
		}
		query := url.Values{"limit": []string{"100"}}
		if continuation != "" {
			query.Set("continue", continuation)
		}
		if err := k.Call(ctx, "GET", base+"?"+query.Encode(), nil, &list); err != nil {
			return nil, err
		}
		for _, item := range list.Items {
			out = append(out, item.Metadata.Name)
		}
		if list.Metadata.Continue == "" {
			return out, nil
		}
		if list.Metadata.Continue == continuation {
			return nil, errors.New("Kubernetes route pagination did not advance")
		}
		continuation = list.Metadata.Continue
	}
}

func (k *Client) DeleteRoute(ctx context.Context, name string) error {
	base := "/apis/gateway.networking.k8s.io/v1/namespaces/" + k.Namespace + "/httproutes"
	if e := k.Call(ctx, "DELETE", base+"/"+name, nil, nil); e != nil && !KubeMissing(e) {
		return e
	}
	return nil
}

// staleWorkerRoutes lists routes with the given prefix that are not desired.
func StaleWorkerRoutes(ctx context.Context, k *Client, prefix string, desired map[string]bool) ([]string, error) {
	names, e := k.ListRoutes(ctx)
	if e != nil {
		return nil, e
	}
	out := []string{}
	for _, n := range names {
		if strings.HasPrefix(n, prefix) && !desired[n] {
			out = append(out, n)
		}
	}
	return out, nil
}

func (k *Client) EnsureRoute(ctx context.Context, name, runtimeService string, g appconfig.WorkerGateway, hosts []string) (bool, error) {
	base := "/apis/gateway.networking.k8s.io/v1/namespaces/" + k.Namespace + "/httproutes"
	if len(hosts) == 0 {
		// Prune: a route with no hostnames would be invalid anyway, and leaving
		// it behind keeps a removed environment reachable.
		e := k.Call(ctx, "DELETE", base+"/"+name, nil, nil)
		if KubeMissing(e) {
			return true, nil
		}
		return true, e
	}
	sort.Strings(hosts)
	gatewayName, gatewayNamespace, section := g.Name, g.Namespace, g.Section
	spec := map[string]any{"parentRefs": []any{map[string]string{"name": gatewayName, "namespace": gatewayNamespace, "sectionName": section}}, "hostnames": hosts, "rules": []any{map[string]any{"filters": []any{map[string]any{"type": "RequestHeaderModifier", "requestHeaderModifier": map[string]any{"remove": []string{"x-krun-scope", "x-krun-version", "x-forwarded-host", "x-internal"}}}}, "backendRefs": []any{map[string]any{"name": runtimeService, "port": 8081}}}}}
	var current struct {
		Metadata struct {
			Generation int `json:"generation"`
		} `json:"metadata"`
		Spec   json.RawMessage `json:"spec"`
		Status struct {
			Parents []struct {
				Parent struct {
					Name      string `json:"name"`
					Namespace string `json:"namespace"`
					Section   string `json:"sectionName"`
				} `json:"parentRef"`
				Conditions []struct {
					Type     string `json:"type"`
					Status   string `json:"status"`
					Observed int    `json:"observedGeneration"`
				} `json:"conditions"`
			} `json:"parents"`
		} `json:"status"`
	}
	e := k.Call(ctx, "GET", base+"/"+name, nil, &current)
	if KubeMissing(e) {
		e = k.Call(ctx, "POST", base, map[string]any{"apiVersion": "gateway.networking.k8s.io/v1", "kind": "HTTPRoute", "metadata": map[string]string{"name": name, "namespace": k.Namespace}, "spec": spec}, nil)
		if KubeConflict(e) {
			return false, nil
		}
		return false, e
	}
	if e != nil {
		return false, e
	}
	var old struct {
		Hosts  []string `json:"hostnames"`
		Parent []struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
			Section   string `json:"sectionName"`
		} `json:"parentRefs"`
		Rules []struct {
			BackendRefs []struct {
				Name string `json:"name"`
			} `json:"backendRefs"`
		} `json:"rules"`
	}
	_ = json.Unmarshal(current.Spec, &old)
	// parentRefs is part of the desired state: re-pointing a route at another
	// Gateway instance is exactly what the network-scope split relies on, so a
	// host-only comparison would silently leave the old attachment in place.
	parentDrift := len(old.Parent) != 1 || old.Parent[0].Name != gatewayName ||
		old.Parent[0].Namespace != gatewayNamespace || old.Parent[0].Section != section
	backendDrift := len(old.Rules) != 1 || len(old.Rules[0].BackendRefs) != 1 || old.Rules[0].BackendRefs[0].Name != runtimeService
	if parentDrift || backendDrift || strings.Join(old.Hosts, "\n") != strings.Join(hosts, "\n") {
		return false, k.Call(ctx, "PATCH", base+"/"+name, map[string]any{"spec": spec}, nil)
	}
	accepted, resolved := false, false
	for _, p := range current.Status.Parents {
		if p.Parent.Name != gatewayName || p.Parent.Namespace != gatewayNamespace || p.Parent.Section != section {
			continue
		}
		for _, condition := range p.Conditions {
			if condition.Status == "True" && condition.Observed == current.Metadata.Generation {
				if condition.Type == "Accepted" {
					accepted = true
				}
				if condition.Type == "ResolvedRefs" {
					resolved = true
				}
			}
		}
	}
	var gateway struct {
		Metadata struct {
			Generation int `json:"generation"`
		} `json:"metadata"`
		Status struct {
			Conditions []struct {
				Type     string `json:"type"`
				Status   string `json:"status"`
				Observed int    `json:"observedGeneration"`
			} `json:"conditions"`
		} `json:"status"`
	}
	if e = k.Call(ctx, "GET", "/apis/gateway.networking.k8s.io/v1/namespaces/"+gatewayNamespace+"/gateways/"+gatewayName, nil, &gateway); e != nil {
		return false, e
	}
	programmed := false
	for _, condition := range gateway.Status.Conditions {
		if condition.Type == "Programmed" && condition.Status == "True" && condition.Observed == gateway.Metadata.Generation {
			programmed = true
		}
	}
	return accepted && resolved && programmed, nil
}

// One shared static route per (domain, Gateway) serves every service. Requests
// are rewritten to path-style object keys so the bucket stays the origin and
// Gateway API needs no per-service rules. Assets are copied into one prefix per
// domain; the route for a domain only exists on that domain's Gateway instance.
func (k *Client) EnsureStaticRoute(ctx context.Context, c *appconfig.WorkersConfig, domainID string, prefix string) error {
	d, ok := c.Domain(domainID)
	if !ok || d.StaticDomain == "" {
		return nil
	}
	g, ok := c.Gateway(d.Gateway)
	if !ok {
		return nil
	}
	host := d.StaticDomain
	// One route per domain: they carry different hostnames and may sit on
	// different Gateway instances.
	name := "workers-static-" + domainID
	gatewayName, gatewayNamespace, section := g.Name, g.Namespace, g.Section
	base := "/apis/gateway.networking.k8s.io/v1/namespaces/" + k.Namespace + "/httproutes"
	spec := map[string]any{
		"parentRefs": []any{map[string]string{"name": gatewayName, "namespace": gatewayNamespace, "sectionName": section}},
		"hostnames":  []string{host},
		"rules": []any{map[string]any{
			"matches": []any{map[string]any{"path": map[string]string{"type": "PathPrefix", "value": "/"}}},
			"filters": []any{
				map[string]any{"type": "URLRewrite", "urlRewrite": map[string]any{"path": map[string]string{"type": "ReplacePrefixMatch", "replacePrefixMatch": "/" + c.S3Bucket + "/" + prefix + "/"}}},
				map[string]any{"type": "ResponseHeaderModifier", "responseHeaderModifier": map[string]any{"set": []map[string]string{{"name": "Access-Control-Allow-Origin", "value": "*"}}}},
			},
			"backendRefs": []any{map[string]any{"name": c.StaticService, "port": c.StaticServicePort}},
		}},
	}
	var current struct {
		Spec json.RawMessage `json:"spec"`
	}
	e := k.Call(ctx, "GET", base+"/"+name, nil, &current)
	if KubeMissing(e) {
		e = k.Call(ctx, "POST", base, map[string]any{"apiVersion": "gateway.networking.k8s.io/v1", "kind": "HTTPRoute", "metadata": map[string]string{"name": name, "namespace": k.Namespace}, "spec": spec}, nil)
		if KubeConflict(e) {
			return nil
		}
		return e
	}
	if e != nil {
		return e
	}
	var old struct {
		Hosts  []string `json:"hostnames"`
		Parent []struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
			Section   string `json:"sectionName"`
		} `json:"parentRefs"`
		Rules []struct {
			Filters []struct {
				URLRewrite *struct {
					Path struct {
						ReplacePrefixMatch string `json:"replacePrefixMatch"`
					} `json:"path"`
				} `json:"urlRewrite"`
			} `json:"filters"`
		} `json:"rules"`
	}
	_ = json.Unmarshal(current.Spec, &old)
	// Re-pointing the sharing route is part of the desired state: a scope moved
	// to another Gateway instance must not keep serving from the old one.
	parentDrift := len(old.Parent) != 1 || old.Parent[0].Name != gatewayName ||
		old.Parent[0].Namespace != gatewayNamespace || old.Parent[0].Section != section
	// The object prefix is part of the desired state too; changing which prefix
	// a domain serves must rewrite the route, not just the hostname.
	want := "/" + c.S3Bucket + "/" + prefix + "/"
	rewriteDrift := true
	if len(old.Rules) == 1 {
		for _, f := range old.Rules[0].Filters {
			if f.URLRewrite != nil && f.URLRewrite.Path.ReplacePrefixMatch == want {
				rewriteDrift = false
			}
		}
	}
	if parentDrift || rewriteDrift || len(old.Hosts) != 1 || old.Hosts[0] != host {
		return k.Call(ctx, "PATCH", base+"/"+name, map[string]any{"spec": spec}, nil)
	}
	return nil
}
