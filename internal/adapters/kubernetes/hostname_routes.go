package kubernetes

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"

	"github.com/krun-dev/krun/internal/config"
)

type HostnameRule struct {
	Match      string
	Path       string
	Service    string
	Namespace  string
	Port       int
	HostHeader string
	Status     int
	Worker     string
}

const hostnameRevision = "krun.dev/hostname-revision"

func (k *Client) EnsureHostnameRoute(ctx context.Context, name, host string, revision int, gateway config.WorkerGateway, rules []HostnameRule) error {
	path := "/apis/gateway.networking.k8s.io/v1/namespaces/" + k.Namespace + "/httproutes"
	var current struct {
		Metadata struct {
			Version     string            `json:"resourceVersion"`
			Annotations map[string]string `json:"annotations"`
		} `json:"metadata"`
		Spec map[string]any `json:"spec"`
	}

	spec := hostnameRouteSpec(host, gateway, rules)
	// Round-trip numbers into the same representation as the API response.
	wire, err := json.Marshal(spec)
	if err != nil {
		return err
	}
	if err = json.Unmarshal(wire, &spec); err != nil {
		return err
	}
	annotations := map[string]string{hostnameRevision: strconv.Itoa(revision)}
	err = k.Call(ctx, "GET", path+"/"+name, nil, &current)
	if KubeMissing(err) {
		err = k.Call(ctx, "POST", path, map[string]any{
			"apiVersion": "gateway.networking.k8s.io/v1", "kind": "HTTPRoute",
			"metadata": map[string]any{"name": name, "namespace": k.Namespace, "annotations": annotations},
			"spec":     spec,
		}, nil)
		if KubeConflict(err) {
			return nil
		}
		return err
	}
	if err != nil {
		return err
	}
	prior, _ := strconv.Atoi(current.Metadata.Annotations[hostnameRevision])
	if prior > revision || (prior == revision && reflect.DeepEqual(normalizeHostnameRouteSpec(current.Spec), normalizeHostnameRouteSpec(spec))) {
		return nil
	}
	err = k.Call(ctx, "PATCH", path+"/"+name, map[string]any{
		"metadata": map[string]any{"resourceVersion": current.Metadata.Version, "annotations": annotations},
		"spec":     spec,
	}, nil)
	if KubeConflict(err) {
		return nil
	}
	return err
}

func hostnameRouteSpec(host string, gateway config.WorkerGateway, rules []HostnameRule) map[string]any {
	entries := make([]any, 0, len(rules))
	for _, rule := range rules {
		pathType := "PathPrefix"
		if rule.Match == "exact" {
			pathType = "Exact"
		}
		entry := map[string]any{
			"matches": []any{map[string]any{"path": map[string]any{"type": pathType, "value": rule.Path}}},
		}
		if rule.Status != 0 {
			entry["filters"] = []any{map[string]any{"type": "ExtensionRef", "extensionRef": map[string]any{
				"group": "gateway.kgateway.dev", "kind": "DirectResponse", "name": managedResponseName(rule.Status),
			}}}
		} else {
			backend := map[string]any{"name": rule.Service, "port": rule.Port}
			if rule.Namespace != "" {
				backend["namespace"] = rule.Namespace
			}
			entry["backendRefs"] = []any{backend}
			remove := []string{"x-krun-scope", "x-krun-version", "x-forwarded-host", "x-internal", "x-krun-route"}
			modifier := map[string]any{"remove": remove}
			if rule.Worker != "" {
				modifier["remove"] = remove[:len(remove)-1]
				modifier["set"] = []any{map[string]string{"name": "x-krun-route", "value": rule.Worker + ":" + rule.Match + ":" + rule.Path}}
			}
			filters := []any{map[string]any{"type": "RequestHeaderModifier", "requestHeaderModifier": modifier}}
			if rule.HostHeader != "" {
				filters = append(filters, map[string]any{"type": "URLRewrite", "urlRewrite": map[string]string{"hostname": rule.HostHeader}})
			}
			entry["filters"] = filters
		}
		entries = append(entries, entry)
	}
	return map[string]any{
		"parentRefs": []any{map[string]string{"name": gateway.Name, "namespace": gateway.Namespace, "sectionName": gateway.Section}},
		"hostnames":  []string{host},
		"rules":      entries,
	}
}

// API defaulting fills these fields even when they are omitted by the client.
func normalizeHostnameRouteSpec(spec map[string]any) map[string]any {
	stripDefaults := func(values any, defaults map[string]any) {
		list, _ := values.([]any)
		for _, value := range list {
			ref, _ := value.(map[string]any)
			for key, expected := range defaults {
				if ref[key] == expected {
					delete(ref, key)
				}
			}
		}
	}
	stripDefaults(spec["parentRefs"], map[string]any{"group": "gateway.networking.k8s.io", "kind": "Gateway"})
	rules, _ := spec["rules"].([]any)
	for _, value := range rules {
		rule, _ := value.(map[string]any)
		stripDefaults(rule["backendRefs"], map[string]any{"group": "", "kind": "Service", "weight": float64(1)})
	}
	return spec
}

func managedResponseName(status int) string { return fmt.Sprintf("krun-hostname-%d", status) }

func (k *Client) EnsureHostnameResponses(ctx context.Context) error {
	path := "/apis/gateway.kgateway.dev/v1alpha1/namespaces/" + k.Namespace + "/directresponses"
	for _, status := range []int{404, 410, 503} {
		name := managedResponseName(status)
		var current struct {
			Spec struct {
				Status int `json:"status"`
			} `json:"spec"`
		}
		err := k.Call(ctx, "GET", path+"/"+name, nil, &current)
		spec := map[string]int{"status": status}
		if KubeMissing(err) {
			err = k.Call(ctx, "POST", path, map[string]any{
				"apiVersion": "gateway.kgateway.dev/v1alpha1", "kind": "DirectResponse",
				"metadata": map[string]string{"name": name, "namespace": k.Namespace}, "spec": spec,
			}, nil)
			if KubeConflict(err) {
				continue
			}
		} else if err == nil && current.Spec.Status != status {
			err = k.Call(ctx, "PATCH", path+"/"+name, map[string]any{"spec": spec}, nil)
		}
		if err != nil {
			return err
		}
	}
	return nil
}
