package runtimebundle

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"maps"
	"net"
	"slices"
	"strings"

	"github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/domain"
	"sigs.k8s.io/yaml"
)

var (
	ErrPoolNotFound                = errors.New("Runtime pool not found")
	ErrPoolDeploymentNotConfigured = errors.New("Runtime pool deployment is not configured")
)

// RenderPoolManifests renders a serving pool from loaded server configuration.
func RenderPoolManifests(c config.Config, poolID string) (domain.RuntimePoolManifests, error) {
	if c.Workers == nil {
		return domain.RuntimePoolManifests{}, ErrPoolNotFound
	}
	pool, ok := c.Workers.RuntimePool(poolID)
	if !ok {
		return domain.RuntimePoolManifests{}, ErrPoolNotFound
	}
	if pool.Deployment == nil {
		return domain.RuntimePoolManifests{}, ErrPoolDeploymentNotConfigured
	}

	namespace, name := c.Workers.Namespace, pool.RuntimeService
	controlAddress := pool.Deployment.ControlAddress
	if controlAddress == "" {
		_, port, err := net.SplitHostPort(c.InternalListen)
		if err != nil {
			return domain.RuntimePoolManifests{}, errors.New("runtime pool requires an internal listener port")
		}
		controlAddress = net.JoinHostPort("krun-control-internal."+namespace+".svc", port)
	}
	networks := append([]string{"public"}, pool.Deployment.StorageEgressCIDRs...)
	text, err := Render(c.Workers, pool, controlAddress, networks, false)
	if err != nil {
		return domain.RuntimePoolManifests{}, fmt.Errorf("render runtime configuration: %w", err)
	}
	if len(text) > 1<<20 {
		return domain.RuntimePoolManifests{}, errors.New("runtime.capnp exceeds the Kubernetes ConfigMap size limit")
	}

	hash := sha256.Sum256([]byte(text))
	configName := fmt.Sprintf("%s-config-%x", name, hash[:8])
	configMap, err := marshalManifest("v1", "ConfigMap", namespace, configName, map[string]any{
		"data":      map[string]string{"runtime.capnp": text},
		"immutable": true,
	})
	if err != nil {
		return domain.RuntimePoolManifests{}, err
	}
	deployment, err := marshalManifest("apps/v1", "Deployment", namespace, name, map[string]any{
		"spec": poolDeploymentSpec(c, pool, configName),
	})
	if err != nil {
		return domain.RuntimePoolManifests{}, err
	}
	supporting, err := renderPoolServices(namespace, pool)
	if err != nil {
		return domain.RuntimePoolManifests{}, err
	}
	return domain.RuntimePoolManifests{Deployment: deployment, ConfigMap: configMap, SupportingResources: supporting}, nil
}

func marshalManifest(apiVersion, kind, namespace, name string, fields map[string]any) (string, error) {
	manifest := map[string]any{
		"apiVersion": apiVersion,
		"kind":       kind,
		"metadata": map[string]any{
			"namespace": namespace,
			"name":      name,
			"labels":    map[string]string{"app.kubernetes.io/part-of": "krun"},
		},
	}
	maps.Copy(manifest, fields)
	raw, err := yaml.Marshal(manifest)
	if err != nil {
		return "", fmt.Errorf("render runtime %s: %w", kind, err)
	}
	return string(raw), nil
}

func poolDeploymentSpec(c config.Config, pool config.WorkerRuntimePool, configName string) map[string]any {
	d := pool.Deployment
	selector := map[string]string{"app": pool.RuntimeService}
	labels := map[string]string{
		"app":                       pool.RuntimeService,
		"app.kubernetes.io/part-of": "krun",
		"pool":                      pool.ID,
		"role":                      "krun-runtime",
	}
	maps.Copy(labels, d.PodLabels)
	volumes, mounts := poolVolumes(pool, configName, c.Workers.ArtifactUsesSTS())
	socket := map[string]int{"port": 8083}
	container := map[string]any{
		"name":            "workerd",
		"image":           d.Image,
		"imagePullPolicy": "IfNotPresent",
		"command":         ServingCommand(),
		"env":             ServingEnvironment(pool, d.PeerService, 1000),
		"ports":           []any{map[string]any{"name": "metrics", "containerPort": 8084}},
		"startupProbe":    map[string]any{"tcpSocket": socket, "periodSeconds": 5, "failureThreshold": 60},
		"readinessProbe": map[string]any{
			"httpGet":        map[string]any{"path": "/healthz", "port": 8083},
			"periodSeconds":  5,
			"timeoutSeconds": 3,
		},
		"livenessProbe": map[string]any{"tcpSocket": socket, "periodSeconds": 10},
		"resources":     d.Resources,
		"securityContext": map[string]any{
			"allowPrivilegeEscalation": false,
			"capabilities":             map[string][]string{"drop": {"ALL"}},
			"readOnlyRootFilesystem":   true,
			"runAsNonRoot":             true,
			"runAsUser":                1000,
			"seccompProfile":           map[string]string{"type": "RuntimeDefault"},
		},
		"volumeMounts": mounts,
	}
	pod := map[string]any{
		"automountServiceAccountToken":  false,
		"enableServiceLinks":            false,
		"terminationGracePeriodSeconds": 60,
		"securityContext":               map[string]any{"fsGroup": 1000, "fsGroupChangePolicy": "OnRootMismatch"},
		"containers":                    []any{container},
		"volumes":                       volumes,
	}
	if len(d.NodeSelector) != 0 {
		pod["nodeSelector"] = d.NodeSelector
	}
	if len(d.Tolerations) != 0 {
		pod["tolerations"] = d.Tolerations
	}
	if d.DNSPolicy != "" {
		pod["dnsPolicy"] = d.DNSPolicy
	}
	if d.DNSConfig != nil {
		pod["dnsConfig"] = d.DNSConfig
	}
	if c.Deployment != nil {
		if c.Deployment.RuntimeClass != "" {
			pod["runtimeClassName"] = c.Deployment.RuntimeClass
		}
		if len(c.Deployment.ImagePullSecrets) != 0 {
			pulls := make([]map[string]string, 0, len(c.Deployment.ImagePullSecrets))
			for _, name := range c.Deployment.ImagePullSecrets {
				pulls = append(pulls, map[string]string{"name": name})
			}
			pod["imagePullSecrets"] = pulls
		}
	}
	return map[string]any{
		"replicas":             d.Replicas,
		"revisionHistoryLimit": 3,
		"selector":             map[string]any{"matchLabels": selector},
		"strategy": map[string]any{
			"type":          "RollingUpdate",
			"rollingUpdate": map[string]int{"maxSurge": 1, "maxUnavailable": 0},
		},
		"template": map[string]any{
			"metadata": map[string]any{"labels": labels},
			"spec":     pod,
		},
	}
}

func poolVolumes(pool config.WorkerRuntimePool, configName string, useSTS bool) ([]any, []any) {
	d := pool.Deployment
	uid := "metadata.uid"
	if d.PodUIDAnnotation != "" {
		uid = fmt.Sprintf("metadata.annotations['%s']", d.PodUIDAnnotation)
	}
	volumes := []any{
		map[string]any{"name": "config", "configMap": map[string]string{"name": configName}},
		poolSecretVolume("secrets", d.SecretName, map[string]string{
			"KRUN_RUNTIME_TOKEN":    "KRUN_RUNTIME_TOKEN",
			"KRUN_MANAGEMENT_TOKEN": "KRUN_MANAGEMENT_TOKEN",
		}),
		map[string]any{
			"name": "pod",
			"downwardAPI": map[string]any{"items": []any{
				map[string]any{"path": "name", "fieldRef": map[string]string{"fieldPath": "metadata.name"}},
				map[string]any{"path": "uid", "fieldRef": map[string]string{"fieldPath": uid}},
			}},
		},
		map[string]any{"name": "tmp", "emptyDir": map[string]string{"sizeLimit": "128Mi"}},
	}
	mounts := []any{
		map[string]any{"name": "config", "mountPath": "/config", "readOnly": true},
		map[string]any{"name": "secrets", "mountPath": "/secrets", "readOnly": true},
		map[string]any{"name": "pod", "mountPath": "/pod", "readOnly": true},
		map[string]any{"name": "tmp", "mountPath": "/tmp"},
	}
	if !useSTS {
		volumes = append(volumes, poolSecretVolume("storage", d.SecretName, map[string]string{
			"artifact-access": "access",
			"artifact-secret": "secret",
		}))
		mounts = append(mounts, map[string]any{"name": "storage", "mountPath": "/storage", "readOnly": true})
	}
	return volumes, mounts
}

func poolSecretVolume(name, secret string, paths map[string]string) map[string]any {
	items := make([]map[string]string, 0, len(paths))
	for _, key := range slices.Sorted(maps.Keys(paths)) {
		items = append(items, map[string]string{"key": key, "path": paths[key]})
	}
	return map[string]any{"name": name, "secret": map[string]any{
		"secretName":  secret,
		"defaultMode": 0440,
		"items":       items,
	}}
}

func renderPoolServices(namespace string, pool config.WorkerRuntimePool) (string, error) {
	name, peer := pool.RuntimeService, pool.Deployment.PeerService
	spec := poolServiceSpec(name, 8081, 8083)
	var resources []string
	appendManifest := func(api, kind, resourceName string, spec map[string]any) error {
		manifest, err := marshalManifest(api, kind, namespace, resourceName, map[string]any{"spec": spec})
		if err != nil {
			return err
		}
		resources = append(resources, manifest)
		return nil
	}

	if name == peer {
		spec = poolServiceSpec(name, 8081, ExecutionPort, 8083)
		spec["clusterIP"] = "None"
	}
	if err := appendManifest("v1", "Service", name, spec); err != nil {
		return "", err
	}
	if name != peer {
		if err := appendManifest("v1", "Service", peer, ServingPeerSpec(name)); err != nil {
			return "", err
		}
	}
	const kgatewayAPI = "gateway.kgateway.dev/v1alpha1"
	if err := appendManifest(kgatewayAPI, "BackendConfigPolicy", name, ServingHashSpec(name)); err != nil {
		return "", err
	}
	if streams := pool.StreamService(); streams != "" {
		if err := appendManifest("v1", "Service", streams, poolServiceSpec(name, 8081)); err != nil {
			return "", err
		}
		if err := appendManifest(kgatewayAPI, "BackendConfigPolicy", streams, ServingStreamSpec(streams)); err != nil {
			return "", err
		}
	}
	return strings.Join(resources, "---\n"), nil
}

func poolServiceSpec(app string, ports ...int) map[string]any {
	spec := make([]any, 0, len(ports))
	for _, port := range ports {
		name := fmt.Sprintf("tcp-%d", port)
		if port == ExecutionPort {
			name = "execution"
		}
		spec = append(spec, map[string]any{"name": name, "protocol": "TCP", "port": port, "targetPort": port})
	}
	return map[string]any{"selector": map[string]string{"app": app}, "ports": spec}
}
