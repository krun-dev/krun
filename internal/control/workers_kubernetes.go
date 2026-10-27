package control

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

type workerKubernetes struct {
	base      string
	tokenFile string
	client    *http.Client
	namespace string
}
type workerKubeError struct {
	status    int
	operation string
}

func (e *workerKubeError) Error() string {
	return fmt.Sprintf("Kubernetes %s returned %d", e.operation, e.status)
}
func newWorkerKubernetes(config *WorkersConfig) (*workerKubernetes, error) {
	token, e := os.ReadFile(config.KubernetesTokenFile)
	if e != nil {
		return nil, e
	}
	ca, e := os.ReadFile(config.KubernetesCAFile)
	if e != nil {
		return nil, e
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return nil, errors.New("invalid Kubernetes CA")
	}
	if strings.TrimSpace(string(token)) == "" {
		return nil, errors.New("empty Kubernetes bearer token")
	}
	return &workerKubernetes{base: config.KubernetesAPI, tokenFile: config.KubernetesTokenFile, namespace: config.Namespace, client: &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}}}, nil
}
func (k *workerKubernetes) call(ctx context.Context, method, route string, in, out any) error {
	var raw []byte
	if in != nil {
		var e error
		raw, e = json.Marshal(in)
		if e != nil {
			return e
		}
	}
	req, e := http.NewRequestWithContext(ctx, method, k.base+route, bytes.NewReader(raw))
	if e != nil {
		return e
	}
	// Projected service-account tokens rotate. Re-reading this tiny local file
	// per API call follows the rotation atomically performed by kubelet and
	// avoids pinning a token that will expire during a long-running controller.
	if k.tokenFile != "" {
		token, readErr := os.ReadFile(k.tokenFile)
		if readErr != nil {
			return readErr
		}
		value := strings.TrimSpace(string(token))
		if value == "" {
			return errors.New("empty Kubernetes bearer token")
		}
		req.Header.Set("Authorization", "Bearer "+value)
	}
	req.Header.Set("Content-Type", "application/json")
	if method == http.MethodPatch {
		req.Header.Set("Content-Type", "application/merge-patch+json")
	}
	resp, e := k.client.Do(req)
	if e != nil {
		return e
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 65536))
		return &workerKubeError{resp.StatusCode, method + " " + strings.Split(route, "?")[0]}
	}
	if out == nil {
		_, e = io.Copy(io.Discard, io.LimitReader(resp.Body, 2<<20))
		return e
	}
	if text, ok := out.(*string); ok {
		data, e := io.ReadAll(io.LimitReader(resp.Body, 32768))
		*text = string(data)
		return e
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out)
}
func kubeMissing(e error) bool {
	var target *workerKubeError
	return errors.As(e, &target) && target.status == 404
}
func kubeConflict(e error) bool {
	var target *workerKubeError
	return errors.As(e, &target) && target.status == 409
}
func (k *workerKubernetes) core(kind string) string {
	return "/api/v1/namespaces/" + k.namespace + "/" + kind
}
func (k *workerKubernetes) jobs() string { return "/apis/batch/v1/namespaces/" + k.namespace + "/jobs" }

type workerJob struct {
	Metadata struct {
		UID string `json:"uid"`
	} `json:"metadata"`
	Status struct {
		Succeeded int `json:"succeeded"`
		Failed    int `json:"failed"`
	} `json:"status"`
}
type workerPod struct {
	Name string
	UID  string
	IP   string
}
type workerPodList struct {
	Items []struct {
		Metadata struct {
			Name              string     `json:"name"`
			UID               string     `json:"uid"`
			DeletionTimestamp *time.Time `json:"deletionTimestamp"`
		} `json:"metadata"`
		Status struct {
			PodIP      string `json:"podIP"`
			Conditions []struct {
				Type   string `json:"type"`
				Status string `json:"status"`
			} `json:"conditions"`
		} `json:"status"`
	} `json:"items"`
}

func (k *workerKubernetes) runtimeMembers(ctx context.Context, service string) ([]workerPod, error) {
	var pods workerPodList
	if e := k.call(ctx, "GET", k.core("pods")+"?labelSelector="+url.QueryEscape("role=krun-runtime,pool=default"), nil, &pods); e != nil {
		return nil, e
	}
	var slices struct {
		Items []struct {
			Endpoints []struct {
				Addresses  []string `json:"addresses"`
				Conditions struct {
					Ready *bool `json:"ready"`
				} `json:"conditions"`
				TargetRef *struct {
					UID string `json:"uid"`
				} `json:"targetRef"`
			} `json:"endpoints"`
		} `json:"items"`
	}
	if e := k.call(ctx, "GET", "/apis/discovery.k8s.io/v1/namespaces/"+k.namespace+"/endpointslices?labelSelector="+url.QueryEscape("kubernetes.io/service-name="+service), nil, &slices); e != nil {
		return nil, e
	}
	ready := map[string]bool{}
	for _, sl := range slices.Items {
		for _, ep := range sl.Endpoints {
			if ep.TargetRef != nil && ep.Conditions.Ready != nil && *ep.Conditions.Ready {
				ready[ep.TargetRef.UID] = true
			}
		}
	}
	out := []workerPod{}
	for _, p := range pods.Items {
		if p.Metadata.DeletionTimestamp != nil || !ready[p.Metadata.UID] || p.Status.PodIP == "" {
			continue
		}
		healthy := false
		for _, c := range p.Status.Conditions {
			if c.Type == "Ready" && c.Status == "True" {
				healthy = true
			}
		}
		if healthy {
			out = append(out, workerPod{p.Metadata.Name, p.Metadata.UID, p.Status.PodIP})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UID < out[j].UID })
	return out, nil
}
func (k *workerKubernetes) ensureBuildJob(ctx context.Context, b WorkerBuild, private WorkerBuildPrivate, sourceURL, uploadURL, image, pullPolicy string) error {
	var job workerJob
	e := k.call(ctx, "GET", k.jobs()+"/"+b.Job, nil, &job)
	if e == nil {
		return nil
	}
	if !kubeMissing(e) {
		return e
	}
	input, _ := json.Marshal(map[string]any{"entrypoint": b.Entrypoint, "source_url": sourceURL, "assets": b.Assets, "upload_url": uploadURL})
	cm := map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": b.Job, "namespace": k.namespace, "labels": map[string]string{"app": "krun-builder"}}, "data": map[string]string{"input.json": string(input)}}
	if e = k.call(ctx, "POST", k.core("configmaps"), cm, nil); e != nil && !kubeConflict(e) {
		return e
	}
	container := map[string]any{"name": "builder", "image": image, "imagePullPolicy": pullPolicy, "command": []string{"node", "/app/build-worker.mjs"}, "resources": map[string]any{"requests": map[string]string{"cpu": "100m", "memory": "128Mi"}, "limits": map[string]string{"cpu": "1", "memory": "512Mi"}}, "securityContext": map[string]any{"runAsUser": 1000, "runAsNonRoot": true, "allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true, "capabilities": map[string]any{"drop": []string{"ALL"}}, "seccompProfile": map[string]string{"type": "RuntimeDefault"}}, "volumeMounts": []any{map[string]any{"name": "input", "mountPath": "/input", "readOnly": true}, map[string]any{"name": "work", "mountPath": "/work"}, map[string]any{"name": "tmp", "mountPath": "/tmp"}}}
	spec := map[string]any{"restartPolicy": "Never", "automountServiceAccountToken": false, "securityContext": map[string]int{"fsGroup": 1000}, "containers": []any{container}, "volumes": []any{map[string]any{"name": "input", "configMap": map[string]string{"name": b.Job}}, map[string]any{"name": "work", "emptyDir": map[string]any{}}, map[string]any{"name": "tmp", "emptyDir": map[string]any{}}}}
	value := map[string]any{"apiVersion": "batch/v1", "kind": "Job", "metadata": map[string]any{"name": b.Job, "namespace": k.namespace, "labels": map[string]string{"app": "krun-builder"}}, "spec": map[string]any{"backoffLimit": 0, "activeDeadlineSeconds": 120, "ttlSecondsAfterFinished": 1800, "template": map[string]any{"metadata": map[string]any{"labels": map[string]string{"app": "krun-builder"}}, "spec": spec}}}
	e = k.call(ctx, "POST", k.jobs(), value, &job)
	if kubeConflict(e) {
		return nil
	}
	return e
}
func (k *workerKubernetes) buildLog(ctx context.Context, job string) string {
	var pods workerPodList
	if k.call(ctx, "GET", k.core("pods")+"?labelSelector="+url.QueryEscape("job-name="+job), nil, &pods) != nil || len(pods.Items) == 0 {
		return "Builder log unavailable"
	}
	var text string
	if k.call(ctx, "GET", k.core("pods")+"/"+pods.Items[0].Metadata.Name+"/log?limitBytes=32768", nil, &text) != nil {
		return "Builder log unavailable"
	}
	return safeWorkerLog(text)
}

// ensureRoutes reconciles one HTTPRoute per (worker, Gateway instance). Grouping
// by Gateway rather than by a network concept is what makes the isolation
// structural: an instance's route table only ever contains the hostnames of the
// domains bound to it, so a forged Host cannot cross instances.
// Gateways with no remaining hosts are pruned.
func (k *workerKubernetes) ensureRoutes(ctx context.Context, c *WorkersConfig, name string, hostsByGateway map[string][]string) (bool, error) {
	ready := true
	desired := map[string]bool{}
	for _, g := range c.Gateways {
		hosts := hostsByGateway[g.ID]
		route := name
		if g.ID != c.Gateways[0].ID {
			route = name + "-" + g.ID
		}
		desired[route] = true
		ok, e := k.ensureRoute(ctx, c, route, g, hosts)
		ready = ready && ok
		if e != nil {
			// One failing Gateway must not skip repairing the others.
			return false, e
		}
	}
	// Routes written under an older catalog naming (a Gateway id that no longer
	// exists) would otherwise keep answering for hosts nobody selected.
	stale, err := staleWorkerRoutes(ctx, k, name+"-", desired)
	if err != nil {
		return false, err
	}
	for _, n := range stale {
		if e := k.deleteRoute(ctx, n); e != nil {
			return false, e
		}
	}
	return ready, nil
}

// listRoutes returns the names of HTTPRoutes in the workers namespace.
func (k *workerKubernetes) listRoutes(ctx context.Context) ([]string, error) {
	var list struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		} `json:"items"`
	}
	base := "/apis/gateway.networking.k8s.io/v1/namespaces/" + k.namespace + "/httproutes"
	if e := k.call(ctx, "GET", base, nil, &list); e != nil {
		return nil, e
	}
	out := make([]string, 0, len(list.Items))
	for _, item := range list.Items {
		out = append(out, item.Metadata.Name)
	}
	return out, nil
}

func (k *workerKubernetes) deleteRoute(ctx context.Context, name string) error {
	base := "/apis/gateway.networking.k8s.io/v1/namespaces/" + k.namespace + "/httproutes"
	if e := k.call(ctx, "DELETE", base+"/"+name, nil, nil); e != nil && !kubeMissing(e) {
		return e
	}
	return nil
}

// staleWorkerRoutes lists routes with the given prefix that are not desired.
func staleWorkerRoutes(ctx context.Context, k *workerKubernetes, prefix string, desired map[string]bool) ([]string, error) {
	names, e := k.listRoutes(ctx)
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

func (k *workerKubernetes) ensureRoute(ctx context.Context, c *WorkersConfig, name string, g WorkerGateway, hosts []string) (bool, error) {
	base := "/apis/gateway.networking.k8s.io/v1/namespaces/" + k.namespace + "/httproutes"
	if len(hosts) == 0 {
		// Prune: a route with no hostnames would be invalid anyway, and leaving
		// it behind keeps a removed environment reachable.
		e := k.call(ctx, "DELETE", base+"/"+name, nil, nil)
		if kubeMissing(e) {
			return true, nil
		}
		return true, e
	}
	sort.Strings(hosts)
	gatewayName, gatewayNamespace, section := g.Name, g.Namespace, g.Section
	spec := map[string]any{"parentRefs": []any{map[string]string{"name": gatewayName, "namespace": gatewayNamespace, "sectionName": section}}, "hostnames": hosts, "rules": []any{map[string]any{"filters": []any{map[string]any{"type": "RequestHeaderModifier", "requestHeaderModifier": map[string]any{"remove": []string{"x-krun-scope", "x-krun-release", "x-forwarded-host", "x-internal"}}}}, "backendRefs": []any{map[string]any{"name": c.RuntimeService, "port": 8081}}}}}
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
	e := k.call(ctx, "GET", base+"/"+name, nil, &current)
	if kubeMissing(e) {
		e = k.call(ctx, "POST", base, map[string]any{"apiVersion": "gateway.networking.k8s.io/v1", "kind": "HTTPRoute", "metadata": map[string]string{"name": name, "namespace": k.namespace}, "spec": spec}, nil)
		if kubeConflict(e) {
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
	}
	_ = json.Unmarshal(current.Spec, &old)
	// parentRefs is part of the desired state: re-pointing a route at another
	// Gateway instance is exactly what the network-scope split relies on, so a
	// host-only comparison would silently leave the old attachment in place.
	parentDrift := len(old.Parent) != 1 || old.Parent[0].Name != gatewayName ||
		old.Parent[0].Namespace != gatewayNamespace || old.Parent[0].Section != section
	if parentDrift || strings.Join(old.Hosts, "\n") != strings.Join(hosts, "\n") {
		return false, k.call(ctx, "PATCH", base+"/"+name, map[string]any{"spec": spec}, nil)
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
	if e = k.call(ctx, "GET", "/apis/gateway.networking.k8s.io/v1/namespaces/"+gatewayNamespace+"/gateways/"+gatewayName, nil, &gateway); e != nil {
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
func (k *workerKubernetes) ensureStaticRoute(ctx context.Context, c *WorkersConfig, domainID string, prefix string) error {
	d, ok := c.domain(domainID)
	if !ok || d.StaticDomain == "" {
		return nil
	}
	g, ok := c.gateway(d.Gateway)
	if !ok {
		return nil
	}
	host := d.StaticDomain
	// One route per domain: they carry different hostnames and may sit on
	// different Gateway instances.
	name := "workers-static-" + domainID
	gatewayName, gatewayNamespace, section := g.Name, g.Namespace, g.Section
	base := "/apis/gateway.networking.k8s.io/v1/namespaces/" + k.namespace + "/httproutes"
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
	e := k.call(ctx, "GET", base+"/"+name, nil, &current)
	if kubeMissing(e) {
		e = k.call(ctx, "POST", base, map[string]any{"apiVersion": "gateway.networking.k8s.io/v1", "kind": "HTTPRoute", "metadata": map[string]string{"name": name, "namespace": k.namespace}, "spec": spec}, nil)
		if kubeConflict(e) {
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
		return k.call(ctx, "PATCH", base+"/"+name, map[string]any{"spec": spec}, nil)
	}
	return nil
}
