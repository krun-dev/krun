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
	token     string
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
func newWorkerKubernetes(ns string) (*workerKubernetes, error) {
	dir := os.Getenv("KRUN_KUBE_SA_DIR")
	if dir == "" {
		dir = "/var/run/secrets/kubernetes.io/serviceaccount/"
	}
	token, e := os.ReadFile(dir + "token")
	if e != nil {
		return nil, e
	}
	caPath := os.Getenv("KRUN_KUBE_CA")
	if caPath == "" {
		caPath = dir + "ca.crt"
	}
	ca, e := os.ReadFile(caPath)
	if e != nil {
		return nil, e
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return nil, errors.New("invalid Kubernetes CA")
	}
	base := os.Getenv("KRUN_KUBE_API")
	if base == "" {
		base = "https://kubernetes.default.svc"
	}
	return &workerKubernetes{base: base, token: strings.TrimSpace(string(token)), namespace: ns, client: &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}}}, nil
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
	req.Header.Set("Authorization", "Bearer "+k.token)
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
func (k *workerKubernetes) ensureRoutes(ctx context.Context, c *WorkersConfig, name string, hosts []string) (bool, error) {
	if len(hosts) == 0 {
		return true, nil
	}
	sort.Strings(hosts)
	base := "/apis/gateway.networking.k8s.io/v1/namespaces/" + k.namespace + "/httproutes"
	spec := map[string]any{"parentRefs": []any{map[string]string{"name": c.GatewayName, "namespace": c.GatewayNamespace, "sectionName": "private"}}, "hostnames": hosts, "rules": []any{map[string]any{"filters": []any{map[string]any{"type": "RequestHeaderModifier", "requestHeaderModifier": map[string]any{"remove": []string{"x-krun-scope", "x-krun-release", "x-forwarded-host", "x-internal"}}}}, "backendRefs": []any{map[string]any{"name": c.RuntimeService, "port": 8081}}}}}
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
		Hosts []string `json:"hostnames"`
	}
	_ = json.Unmarshal(current.Spec, &old)
	if strings.Join(old.Hosts, "\n") != strings.Join(hosts, "\n") {
		return false, k.call(ctx, "PATCH", base+"/"+name, map[string]any{"spec": spec}, nil)
	}
	accepted, resolved := false, false
	for _, p := range current.Status.Parents {
		if p.Parent.Name != c.GatewayName || p.Parent.Namespace != c.GatewayNamespace || p.Parent.Section != "private" {
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
	if e = k.call(ctx, "GET", "/apis/gateway.networking.k8s.io/v1/namespaces/"+c.GatewayNamespace+"/gateways/"+c.GatewayName, nil, &gateway); e != nil {
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

// One shared static route per network scope serves every service. Requests are
// rewritten to path-style object keys (/<bucket>/<pub|int>/...) so the bucket
// stays the origin and Gateway API needs no per-service rules.
func (k *workerKubernetes) ensureStaticRoute(ctx context.Context, c *WorkersConfig, host, prefix string) error {
	if host == "" {
		return nil
	}
	name := "workers-static-" + prefix
	base := "/apis/gateway.networking.k8s.io/v1/namespaces/" + k.namespace + "/httproutes"
	spec := map[string]any{
		"parentRefs": []any{map[string]string{"name": c.GatewayName, "namespace": c.GatewayNamespace, "sectionName": "private"}},
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
		Hosts []string `json:"hostnames"`
	}
	_ = json.Unmarshal(current.Spec, &old)
	if len(old.Hosts) != 1 || old.Hosts[0] != host {
		return k.call(ctx, "PATCH", base+"/"+name, map[string]any{"spec": spec}, nil)
	}
	return nil
}
