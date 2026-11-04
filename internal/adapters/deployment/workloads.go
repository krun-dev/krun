package deployment

import (
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

func security() map[string]any {
	return map[string]any{
		"runAsNonRoot":             true,
		"runAsUser":                1000,
		"allowPrivilegeEscalation": false,
		"readOnlyRootFilesystem":   true,
		"capabilities":             map[string]any{"drop": []string{"ALL"}},
		"seccompProfile": map[string]string{
			"type": "RuntimeDefault",
		},
	}
}
func resources(cpu, memory, limitCPU, limitMemory string) map[string]any {
	return map[string]any{
		"requests": map[string]string{"cpu": cpu, "memory": memory},
		"limits": map[string]string{
			"cpu":    limitCPU,
			"memory": limitMemory,
		},
	}
}
func mount(name, path string) map[string]any {
	return map[string]any{"name": name, "mountPath": path, "readOnly": true}
}
func secretVolume(name, secret string) map[string]any {
	return map[string]any{
		"name": name,
		"secret": map[string]any{
			"secretName":  secret,
			"defaultMode": 0440,
		},
	}
}
func configVolume(name, config string) map[string]any {
	return map[string]any{"name": name, "configMap": map[string]string{"name": config}}
}
func tempVolume(name, size string) map[string]any {
	return map[string]any{"name": name, "emptyDir": map[string]string{"sizeLimit": size}}
}
func tempMount(name, path string) map[string]any {
	return map[string]any{"name": name, "mountPath": path}
}
func probe(path string, port int) map[string]any {
	return map[string]any{
		"httpGet":          map[string]any{"path": path, "port": port},
		"periodSeconds":    5,
		"timeoutSeconds":   3,
		"failureThreshold": 3,
	}
}
func labels(app, role, pool string) map[string]string {
	out := map[string]string{"app": app, "app.kubernetes.io/part-of": "krun"}
	if role != "" {
		out["role"] = role
	}
	if pool != "" {
		out["pool"] = pool
	}
	return out
}
func (r *renderer) deployment(name, role string, count int, container map[string]any, volumes []any, annotations map[string]string, token bool) {
	pool := ""
	if role == "krun-runtime" || role == "krun-validator" {
		pool = r.pool.ID
	}
	pod := map[string]any{
		"automountServiceAccountToken":  token,
		"enableServiceLinks":            false,
		"terminationGracePeriodSeconds": 60,
		"securityContext":               map[string]any{"fsGroup": 1000, "fsGroupChangePolicy": "OnRootMismatch"},
		"containers":                    []any{container},
		"volumes":                       volumes,
		"topologySpreadConstraints": []any{
			map[string]any{
				"maxSkew":           1,
				"topologyKey":       "kubernetes.io/hostname",
				"whenUnsatisfiable": "ScheduleAnyway",
				"labelSelector": map[string]any{
					"matchLabels": map[string]string{
						"app": name,
					},
				},
			},
		},
	}
	if token {
		pod["serviceAccountName"] = "krun-control"
	}
	if pool != "" && r.cfg.Deployment.RuntimeClass != "" {
		pod["runtimeClassName"] = r.cfg.Deployment.RuntimeClass
	}
	pulls := []any{}
	for _, name := range r.cfg.Deployment.ImagePullSecrets {
		pulls = append(pulls, map[string]string{"name": name})
	}
	if len(pulls) > 0 {
		pod["imagePullSecrets"] = pulls
	}
	container["securityContext"] = security()
	r.add("apps/v1", "Deployment", name, map[string]any{
		"spec": map[string]any{
			"replicas":             count,
			"revisionHistoryLimit": 3,
			"selector": map[string]any{
				"matchLabels": map[string]string{
					"app": name,
				},
			},
			"strategy": map[string]any{
				"type": "RollingUpdate",
				"rollingUpdate": map[string]int{
					"maxSurge":       1,
					"maxUnavailable": 0,
				},
			},
			"template": map[string]any{
				"metadata": map[string]any{
					"labels":      labels(name, role, pool),
					"annotations": annotations,
				},
				"spec": pod,
			},
		},
	})
	if count > 1 {
		r.add("policy/v1", "PodDisruptionBudget", name, map[string]any{
			"spec": map[string]any{
				"minAvailable": 1,
				"selector": map[string]any{
					"matchLabels": map[string]string{
						"app": name,
					},
				},
			},
		})
	}
}
func (r *renderer) service(name, app string, ports ...int) {
	values := []any{}
	for _, port := range ports {
		values = append(values, map[string]any{
			"name":       "tcp-" + strconv.Itoa(port),
			"port":       port,
			"targetPort": port,
			"protocol":   "TCP",
		})
	}
	r.add("v1", "Service", name, map[string]any{
		"spec": map[string]any{
			"type": "ClusterIP",
			"selector": map[string]string{
				"app": app,
			},
			"ports": values,
		},
	})
}
func (r *renderer) controlDeployment(config string) {
	r.deployment("krun-control", "krun-control", r.cfg.Deployment.ControlReplicas, map[string]any{
		"name":            "control",
		"image":           r.cfg.Deployment.ControlImage,
		"imagePullPolicy": "IfNotPresent",
		"args":            []string{"serve", "--config", "/config/control.yaml"},
		"ports": []any{
			map[string]any{"containerPort": integerPort(r.cfg.Listen)},
			map[string]any{
				"containerPort": integerPort(r.cfg.InternalListen),
			},
		},
		"readinessProbe": probe("/api/healthz", integerPort(r.cfg.Listen)),
		"livenessProbe": map[string]any{
			"tcpSocket":     map[string]int{"port": integerPort(r.cfg.Listen)},
			"periodSeconds": 10,
		},
		"startupProbe": map[string]any{
			"tcpSocket":        map[string]int{"port": integerPort(r.cfg.Listen)},
			"periodSeconds":    5,
			"failureThreshold": 60,
		},
		"volumeMounts": []any{
			mount("config", "/config"),
			tempMount("work", "/workdir"),
			tempMount("tmp", "/tmp"),
		},
		"resources": resources("250m", "256Mi", "2", "2Gi"),
	}, []any{
		secretVolume("config", "krun-control-config"),
		tempVolume("work", "2Gi"),
		tempVolume("tmp", "256Mi"),
	}, map[string]string{
		"krun.dev/config-hash": hash(config),
	}, true)
	r.service("krun-control", "krun-control", integerPort(r.cfg.Listen))
	r.service("krun-control-internal", "krun-control", integerPort(r.cfg.InternalListen))
}
func (r *renderer) runtimeDeployment(text string, validator bool) {
	role := "krun-runtime"
	name := r.pool.RuntimeService
	token := r.pool.RuntimeToken
	replicas := r.cfg.Deployment.RuntimeReplicas
	if validator {
		role = "krun-validator"
		name = r.pool.ValidatorService
		token = r.pool.ValidatorToken
		replicas = r.cfg.Deployment.ValidatorReplicas
	}
	secret := map[string]string{
		"KRUN_RUNTIME_TOKEN":    token,
		"KRUN_MANAGEMENT_TOKEN": r.pool.ManagementToken,
	}
	volumes := []any{
		configVolume("config", name),
		secretVolume("secrets", name),
		map[string]any{
			"name": "pod",
			"downwardAPI": map[string]any{
				"items": []any{
					map[string]any{"path": "name", "fieldRef": map[string]string{"fieldPath": "metadata.name"}},
					map[string]any{"path": "uid", "fieldRef": map[string]string{"fieldPath": "metadata.uid"}},
				},
			},
		},
	}
	mounts := []any{mount("config", "/config"), mount("secrets", "/secrets"), mount("pod", "/pod")}
	// Artifact signing credentials belong only to the trusted serving entrypoint.
	// R2 issuer credentials never enter either runtime Secret.
	if !validator {
		secret["artifact-access"] = r.cfg.Workers.S3AccessKey
		secret["artifact-secret"] = r.cfg.Workers.S3SecretKey
		volume := secretVolume("storage", name)
		volume["secret"].(map[string]any)["items"] = []any{
			map[string]string{
				"key":  "artifact-access",
				"path": "access",
			},
			map[string]string{
				"key":  "artifact-secret",
				"path": "secret",
			},
		}
		volumes = append(volumes, volume)
		mounts = append(mounts, mount("storage", "/storage"))
	}
	r.add("v1", "Secret", name, map[string]any{"type": "Opaque", "data": secretData(secret)})
	r.add("v1", "ConfigMap", name, map[string]any{"data": map[string]string{"runtime.capnp": text}})
	r.deployment(name, role, replicas, map[string]any{
		"name":            "workerd",
		"image":           r.cfg.Deployment.RuntimeImage,
		"imagePullPolicy": "IfNotPresent",
		"readinessProbe":  probe("/ready", 8083),
		"livenessProbe":   map[string]any{"tcpSocket": map[string]int{"port": 8083}, "periodSeconds": 10},
		"startupProbe": map[string]any{
			"tcpSocket":        map[string]int{"port": 8083},
			"periodSeconds":    5,
			"failureThreshold": 60,
		},
		"volumeMounts": mounts,
		"resources":    resources("250m", "256Mi", "2", "1Gi"),
	}, volumes, map[string]string{
		"krun.dev/config-hash": hash(text),
		"krun.dev/secret-hash": hash(secret),
	}, false)
	if validator {
		r.service(name, name, 8083)
	} else {
		r.service(name, name, 8081, 8083)
	}
}

func (r *renderer) rbac() {
	pulls := []any{}
	for _, name := range r.cfg.Deployment.ImagePullSecrets {
		pulls = append(pulls, map[string]string{"name": name})
	}
	for _, name := range []string{"krun-control", "default"} {
		r.add("v1", "ServiceAccount", name, map[string]any{
			"automountServiceAccountToken": false,
			"imagePullSecrets":             pulls,
		})
	}
	r.add("rbac.authorization.k8s.io/v1", "Role", "krun-control", map[string]any{
		"rules": []any{
			map[string]any{
				"apiGroups": []string{""},
				"resources": []string{"pods", "pods/log"},
				"verbs": []string{
					"get",
					"list",
				},
			},
			map[string]any{
				"apiGroups": []string{""},
				"resources": []string{"secrets"},
				"verbs": []string{
					"get",
					"create",
					"patch",
					"delete",
				},
			},
			map[string]any{
				"apiGroups": []string{"batch"},
				"resources": []string{"jobs"},
				"verbs": []string{
					"get",
					"list",
					"create",
					"patch",
					"delete",
				},
			},
			map[string]any{
				"apiGroups": []string{"discovery.k8s.io"},
				"resources": []string{"endpointslices"},
				"verbs": []string{
					"get",
					"list",
				},
			},
			map[string]any{
				"apiGroups": []string{"gateway.networking.k8s.io"},
				"resources": []string{
					"httproutes",
				},
				"verbs": []string{
					"get",
					"list",
					"create",
					"patch",
					"delete",
				},
			},
		},
	})
	binding := func(role string) map[string]any {
		return map[string]any{
			"roleRef": map[string]string{
				"apiGroup": "rbac.authorization.k8s.io",
				"kind":     "Role",
				"name":     role,
			},
			"subjects": []any{
				map[string]string{
					"kind":      "ServiceAccount",
					"name":      "krun-control",
					"namespace": r.ns,
				},
			},
		}
	}
	r.add("rbac.authorization.k8s.io/v1", "RoleBinding", "krun-control", binding("krun-control"))
	name := "krun-" + r.ns + "-gateway-reader"
	if len(name) > 63 {
		name = "krun-" + hash(r.ns)[:16] + "-gateway-reader"
	}
	byNamespace := map[string][]string{}
	for _, gateway := range r.cfg.Workers.Gateways {
		byNamespace[gateway.Namespace] = append(byNamespace[gateway.Namespace], gateway.Name)
	}
	namespaces := make([]string, 0, len(byNamespace))
	for namespace := range byNamespace {
		namespaces = append(namespaces, namespace)
	}
	sort.Strings(namespaces)
	for _, namespace := range namespaces {
		r.items = append(r.items, r.resource("rbac.authorization.k8s.io/v1", "Role", name, namespace, map[string]any{
			"rules": []any{map[string]any{
				"apiGroups":     []string{"gateway.networking.k8s.io"},
				"resources":     []string{"gateways"},
				"resourceNames": byNamespace[namespace],
				"verbs":         []string{"get"},
			}},
		}))
		r.items = append(r.items, r.resource("rbac.authorization.k8s.io/v1", "RoleBinding", name, namespace, binding(name)))
	}
}

func (r *renderer) consoleResources() {
	text := r.nginx()
	r.add("v1", "ConfigMap", "krun-console", map[string]any{
		"data": map[string]string{
			"nginx.conf": text,
		},
	})
	r.deployment("krun-console", "krun-console", 1, map[string]any{
		"name":            "nginx",
		"image":           r.cfg.Deployment.ConsoleImage,
		"imagePullPolicy": "IfNotPresent",
		"command":         []string{"nginx"},
		"args":            []string{"-c", "/config/nginx.conf", "-g", "daemon off;"},
		"readinessProbe":  probe("/", 8080),
		"volumeMounts": []any{
			mount("config", "/config"),
			tempMount("tmp", "/tmp"),
		},
		"resources": resources("50m", "32Mi", "1", "256Mi"),
	}, []any{configVolume("config", "krun-console"), tempVolume("tmp", "256Mi")}, map[string]string{
		"krun.dev/config-hash": hash(text),
	}, false)
	r.service("krun-console", "krun-console", 8080)
	r.add("gateway.networking.k8s.io/v1", "HTTPRoute", "krun-console", map[string]any{
		"spec": map[string]any{
			"parentRefs": []any{
				map[string]string{
					"name":        r.gateway.Name,
					"namespace":   r.gateway.Namespace,
					"sectionName": r.gateway.Section,
				},
			},
			"hostnames": []string{consoleHost(r.cfg)},
			"rules": []any{
				map[string]any{
					"matches": []any{
						map[string]any{
							"path": map[string]string{
								"type":  "PathPrefix",
								"value": "/api",
							},
						},
					},
					"backendRefs": []any{
						map[string]any{
							"name": "krun-control",
							"port": integerPort(r.cfg.Listen),
						},
					},
				},
				map[string]any{
					"matches": []any{
						map[string]any{
							"path": map[string]string{
								"type":  "PathPrefix",
								"value": "/",
							},
						},
					},
					"backendRefs": []any{
						map[string]any{
							"name": "krun-console",
							"port": 8080,
						},
					},
				},
			},
		},
	})
}

func (r *renderer) nginx() string {
	sources := []string{"'self'"}
	if r.cfg.Workers.ObjectStorage != nil {
		for _, store := range r.cfg.Workers.ObjectStorage.Stores {
			u, _ := url.Parse(store.Endpoint)
			if store.AddressingStyle == "virtual" {
				host := store.Bucket + "." + u.Hostname()
				if u.Port() != "" {
					host = net.JoinHostPort(host, u.Port())
				}
				u.Host = host
			}
			sources = append(sources, u.Scheme+"://"+u.Host)
		}
	}
	resolver := r.cfg.Deployment.DNSResolver
	if strings.Contains(resolver, ":") {
		resolver = "[" + resolver + "]"
	}
	origin, _ := url.Parse(r.cfg.Deployment.ConsoleOrigin)
	staticBase := origin.Host
	if r.cfg.Deployment.ConsolePrefix != "" {
		staticBase += "/" + r.cfg.Deployment.ConsolePrefix
	}
	return fmt.Sprintf(`worker_processes auto;
pid /tmp/nginx.pid;
error_log /dev/stderr warn;
events { worker_connections 1024; }
http {
  access_log /dev/stdout;
  client_body_temp_path /tmp/client;
  proxy_temp_path /tmp/proxy;
  fastcgi_temp_path /tmp/fastcgi;
  uwsgi_temp_path /tmp/uwsgi;
  scgi_temp_path /tmp/scgi;
  resolver %s valid=30s;
  server {
    listen 8080;
    server_name _;
    set $static_origin %s;
    proxy_ssl_server_name on;
    proxy_ssl_name %s;
    proxy_ssl_verify on;
    proxy_ssl_trusted_certificate /etc/ssl/certs/ca-certificates.crt;
    proxy_ssl_protocols TLSv1.2 TLSv1.3;
    proxy_set_header Host %s;
    proxy_set_header Cookie "";
    proxy_set_header Authorization "";
    proxy_hide_header Set-Cookie;
    proxy_hide_header Access-Control-Allow-Origin;
    location = / {
      proxy_pass https://$static_origin/index.html;
      proxy_hide_header Cache-Control;
      add_header Cache-Control "no-store" always;
      add_header X-Content-Type-Options "nosniff" always;
      add_header Referrer-Policy "no-referrer" always;
      add_header Content-Security-Policy "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src %s; frame-ancestors 'none'; base-uri 'self'; form-action 'self'; object-src 'none'" always;
    }
    location ^~ /assets/ {
      proxy_pass https://$static_origin$request_uri;
      proxy_hide_header Cache-Control;
      add_header Cache-Control "public, max-age=31536000, immutable";
      add_header X-Content-Type-Options "nosniff" always;
    }
    location / { return 404; }
  }
}
`, resolver, staticBase, origin.Hostname(), origin.Host, strings.Join(sources, " "))
}
