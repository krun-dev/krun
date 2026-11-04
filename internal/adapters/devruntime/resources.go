package devruntime

func kubernetesResource(kind, name string, fields map[string]any) map[string]any {
	resource := map[string]any{
		"apiVersion": "v1",
		"kind":       kind,
		"metadata": map[string]string{
			"name":      name,
			"namespace": "workers-mvp",
		},
	}
	for key, value := range fields {
		resource[key] = value
	}
	return resource
}

func resourceList(items []any) map[string]any {
	return map[string]any{
		"apiVersion": "v1",
		"kind":       "List",
		"items":      items,
	}
}

func (o renderOptions) secretResources() map[string]any {
	secrets := []any{
		kubernetesResource("Secret", "workers-runtime", map[string]any{
			"type": "Opaque",
			"stringData": map[string]string{
				"KRUN_RUNTIME_TOKEN":    o.pool.RuntimeToken,
				"KRUN_VALIDATOR_TOKEN":  o.pool.ValidatorToken,
				"KRUN_MANAGEMENT_TOKEN": o.pool.ManagementToken,
			},
		}),
	}

	if o.signed {
		storageSecret := kubernetesResource("Secret", "workers-storage", map[string]any{
			"type": "Opaque",
			"stringData": map[string]string{
				"access": o.workers.S3AccessKey,
				"secret": o.workers.S3SecretKey,
			},
		})
		secrets = append(secrets, storageSecret)
	}

	return resourceList(secrets)
}

func (o renderOptions) runtimePodPatch() map[string]any {
	// The patch preserves pod identity and runtime credentials. Storage
	// credentials are mounted separately when runtime reads use signed access.
	volumes := []any{
		map[string]any{
			"name": "config",
			"configMap": map[string]string{
				"name": "runtime-config",
			},
		},
		map[string]any{
			"name": "pod",
			"downwardAPI": map[string]any{
				"items": []any{
					map[string]any{
						"path": "name",
						"fieldRef": map[string]string{
							"fieldPath": "metadata.name",
						},
					},
					map[string]any{
						"path": "uid",
						"fieldRef": map[string]string{
							"fieldPath": "metadata.uid",
						},
					},
				},
			},
		},
		map[string]any{
			"name": "secrets",
			"secret": map[string]any{
				"secretName": "workers-runtime",
				"items": []any{
					map[string]string{
						"key":  "KRUN_RUNTIME_TOKEN",
						"path": "KRUN_RUNTIME_TOKEN",
					},
					map[string]string{
						"key":  "KRUN_MANAGEMENT_TOKEN",
						"path": "KRUN_MANAGEMENT_TOKEN",
					},
				},
			},
		},
	}

	mounts := []any{
		map[string]any{"name": "config", "mountPath": "/config", "readOnly": true},
		map[string]any{"name": "pod", "mountPath": "/pod", "readOnly": true},
		map[string]any{"name": "secrets", "mountPath": "/secrets", "readOnly": true},
	}
	if o.signed {
		volumes = append(volumes, map[string]any{
			"name": "storage",
			"secret": map[string]string{
				"secretName": "workers-storage",
			},
		})
		mounts = append(mounts, map[string]any{
			"name":      "storage",
			"mountPath": "/storage",
			"readOnly":  true,
		})
	}

	container := map[string]any{
		"name":         "workerd",
		"volumeMounts": mounts,
	}
	podSpec := map[string]any{
		"volumes":    volumes,
		"containers": []any{container},
	}
	return map[string]any{
		"spec": map[string]any{
			"template": map[string]any{
				"spec": podSpec,
			},
		},
	}
}
