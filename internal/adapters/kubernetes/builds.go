package kubernetes

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	workerartifact "github.com/krun-dev/krun/internal/artifact"
	appconfig "github.com/krun-dev/krun/internal/config"
	domain "github.com/krun-dev/krun/internal/domain"
)

type WorkerJob struct {
	Metadata struct {
		UID         string            `json:"uid"`
		Annotations map[string]string `json:"annotations"`
	} `json:"metadata"`
	Status struct {
		Succeeded int `json:"succeeded"`
		Failed    int `json:"failed"`
	} `json:"status"`
}

func (k *Client) EnsureBuildJob(ctx context.Context, b domain.WorkerBuild, private domain.WorkerBuildPrivate, sourceURL, uploadURL, image, pullPolicy string) error {
	buildMode := "direct"
	configPath := ""
	assets := b.Assets
	if b.Repository != nil {
		buildMode = "wrangler"
		configPath = b.Repository.ConfigPath
		// Wrangler is authoritative for repository assets.  Do not let stale
		// service metadata override the checked-out project's configuration.
		assets = nil
		if configPath == "" {
			return errors.New("repository build requires an explicit Wrangler config")
		}
	}
	buildInput, e := json.Marshal(map[string]any{
		"build_mode":          buildMode,
		"config_path":         configPath,
		"entrypoint":          b.Entrypoint,
		"source_url":          sourceURL,
		"assets":              assets,
		"compatibility_date":  b.CompatibilityDate,
		"compatibility_flags": b.CompatibilityFlags,
	})
	if e != nil {
		return e
	}
	// The untrusted build phase never receives this payload. In particular it
	// cannot race the trusted packer by writing to the one-shot upload URL.
	packInput, e := json.Marshal(map[string]any{
		"build_mode":          buildMode,
		"config_path":         configPath,
		"upload_url":          uploadURL,
		"compatibility_date":  b.CompatibilityDate,
		"compatibility_flags": b.CompatibilityFlags,
	})
	if e != nil {
		return e
	}
	secretData := map[string]string{"build.json": string(buildInput), "pack.json": string(packInput)}
	if buildMode == "direct" {
		// Direct uploads only run the platform's fixed esbuild pipeline. They do
		// not execute repository build or lifecycle scripts, so they retain the
		// original single-container contract and can upload their own result.
		directInput, marshalErr := json.Marshal(map[string]any{
			"build_mode":          buildMode,
			"entrypoint":          b.Entrypoint,
			"source_url":          sourceURL,
			"assets":              assets,
			"compatibility_date":  b.CompatibilityDate,
			"compatibility_flags": b.CompatibilityFlags,
			"upload_url":          uploadURL,
		})
		if marshalErr != nil {
			return marshalErr
		}
		secretData = map[string]string{"input.json": string(directInput)}
	}
	secret := map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata": map[string]any{
			"name":      b.Job,
			"namespace": k.Namespace,
			"labels":    map[string]string{"app": "krun-builder"},
		},
		"type":       "Opaque",
		"stringData": secretData,
	}
	var job WorkerJob
	e = k.Call(ctx, "GET", k.Jobs()+"/"+b.Job, nil, &job)
	if e == nil {
		if createErr := k.Call(ctx, "POST", k.Core("secrets"), secret, nil); createErr != nil && !KubeConflict(createErr) {
			return createErr
		}
		return k.AdoptBuildInput(ctx, b.Job, job)
	}
	if !KubeMissing(e) {
		return e
	}
	if e = k.Call(ctx, "POST", k.Core("secrets"), secret, nil); KubeConflict(e) {
		// A previous reconcile may have created the input before failing to
		// create the Job. Refresh the short-lived presigned URLs in that case.
		e = k.Call(ctx, http.MethodPatch, k.Core("secrets")+"/"+b.Job, map[string]any{"stringData": secret["stringData"]}, nil)
	}
	if e != nil {
		return e
	}
	registry := k.NpmRegistry
	if registry == "" {
		registry = "https://registry.npmjs.org/"
	}
	timeout := k.timeoutSeconds
	if timeout == 0 {
		timeout = 600
	}
	commandTimeout := appconfig.WorkerBuilderCommandTimeoutMillis(timeout)
	securityContext := func() map[string]any {
		return map[string]any{"runAsUser": 1000, "runAsGroup": 1000, "runAsNonRoot": true, "allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true, "capabilities": map[string]any{"drop": []string{"ALL"}}, "seccompProfile": map[string]string{"type": "RuntimeDefault"}}
	}
	command := []string{"node", "/app/build-worker.mjs"}
	buildContainer := map[string]any{
		"name":            "builder-build",
		"image":           image,
		"imagePullPolicy": pullPolicy,
		"command":         command,
		"env": []map[string]string{
			{"name": "KRUN_BUILD_PHASE", "value": "build"},
			{"name": "KRUN_NPM_REGISTRY", "value": registry},
			{"name": "KRUN_BUILD_TIMEOUT_MS", "value": commandTimeout},
		},
		"resources": map[string]any{
			"requests": map[string]string{"cpu": "100m", "memory": "256Mi", "ephemeral-storage": WorkerBuilderEphemeralRequest},
			"limits":   map[string]string{"cpu": "2", "memory": "2Gi", "ephemeral-storage": WorkerBuilderEphemeralLimit},
		},
		"securityContext": securityContext(),
		"volumeMounts": []any{
			map[string]any{"name": "build-input", "mountPath": "/input", "readOnly": true},
			map[string]any{"name": "work", "mountPath": "/work"},
			map[string]any{"name": "build-tmp", "mountPath": "/tmp"},
		},
	}
	packContainer := map[string]any{
		"name":            "builder-pack",
		"image":           image,
		"imagePullPolicy": pullPolicy,
		"command":         command,
		"env":             []map[string]string{{"name": "KRUN_BUILD_PHASE", "value": "pack"}},
		"resources": map[string]any{
			"requests": map[string]string{"cpu": "50m", "memory": "128Mi", "ephemeral-storage": "64Mi"},
			"limits":   map[string]string{"cpu": "1", "memory": "512Mi", "ephemeral-storage": "512Mi"},
		},
		"securityContext": securityContext(),
		"volumeMounts": []any{
			map[string]any{"name": "pack-input", "mountPath": "/input", "readOnly": true},
			map[string]any{"name": "work", "mountPath": "/work"},
			map[string]any{"name": "pack-tmp", "mountPath": "/tmp"},
		},
	}
	secretVolume := func(key string) map[string]any {
		return map[string]any{"secretName": b.Job, "defaultMode": 0o400, "items": []any{map[string]any{"key": key, "path": "input.json"}}}
	}
	spec := map[string]any{
		"restartPolicy":                 "Never",
		"automountServiceAccountToken":  false,
		"enableServiceLinks":            false,
		"terminationGracePeriodSeconds": 5,
		"securityContext":               map[string]any{"fsGroup": 1000, "fsGroupChangePolicy": "OnRootMismatch"},
	}
	if buildMode == "wrangler" {
		spec["initContainers"] = []any{buildContainer}
		spec["containers"] = []any{packContainer}
		spec["volumes"] = []any{
			map[string]any{"name": "build-input", "secret": secretVolume("build.json")},
			map[string]any{"name": "pack-input", "secret": secretVolume("pack.json")},
			map[string]any{"name": "work", "emptyDir": map[string]any{"sizeLimit": WorkerBuilderWorkSize}},
			map[string]any{"name": "build-tmp", "emptyDir": map[string]any{"sizeLimit": WorkerBuilderTmpSize}},
			map[string]any{"name": "pack-tmp", "emptyDir": map[string]any{"sizeLimit": "64Mi"}},
		}
	} else {
		buildContainer["name"] = "builder-pack"
		buildContainer["env"] = []map[string]string{
			{"name": "KRUN_BUILD_PHASE", "value": "all"},
			{"name": "KRUN_NPM_REGISTRY", "value": registry},
			{"name": "KRUN_BUILD_TIMEOUT_MS", "value": commandTimeout},
		}
		buildContainer["volumeMounts"] = []any{
			map[string]any{"name": "direct-input", "mountPath": "/input", "readOnly": true},
			map[string]any{"name": "work", "mountPath": "/work"},
			map[string]any{"name": "build-tmp", "mountPath": "/tmp"},
		}
		spec["containers"] = []any{buildContainer}
		spec["volumes"] = []any{
			map[string]any{"name": "direct-input", "secret": secretVolume("input.json")},
			map[string]any{"name": "work", "emptyDir": map[string]any{"sizeLimit": WorkerBuilderWorkSize}},
			map[string]any{"name": "build-tmp", "emptyDir": map[string]any{"sizeLimit": WorkerBuilderTmpSize}},
		}
	}
	if k.runtimeClass != "" {
		spec["runtimeClassName"] = k.runtimeClass
	}
	value := map[string]any{"apiVersion": "batch/v1", "kind": "Job", "metadata": map[string]any{"name": b.Job, "namespace": k.Namespace, "labels": map[string]string{"app": "krun-builder"}}, "spec": map[string]any{"backoffLimit": 0, "activeDeadlineSeconds": timeout, "ttlSecondsAfterFinished": 1800, "template": map[string]any{"metadata": map[string]any{"labels": map[string]string{"app": "krun-builder"}}, "spec": spec}}}
	e = k.Call(ctx, "POST", k.Jobs(), value, &job)
	if KubeConflict(e) {
		if e = k.Call(ctx, "GET", k.Jobs()+"/"+b.Job, nil, &job); e != nil {
			return e
		}
	} else if e != nil {
		// The Secret cannot have an ownerReference until the Job has a UID. Do
		// not leak it forever when Job creation fails permanently.
		_ = k.Call(ctx, http.MethodDelete, k.Core("secrets")+"/"+b.Job, nil, nil)
		return e
	}
	return k.AdoptBuildInput(ctx, b.Job, job)
}

func (k *Client) AdoptBuildInput(ctx context.Context, name string, job WorkerJob) error {
	const AdoptedAnnotation = "workers.krun.dev/input-adopted"
	if job.Metadata.UID == "" {
		return errors.New("Kubernetes Job has no UID")
	}
	owner := map[string]any{
		"apiVersion": "batch/v1",
		"kind":       "Job",
		"name":       name,
		"uid":        job.Metadata.UID,
		"controller": true,
	}
	if err := k.Call(ctx, http.MethodPatch, k.Core("secrets")+"/"+name, map[string]any{"metadata": map[string]any{"ownerReferences": []any{owner}}}, nil); err != nil {
		return err
	}
	if job.Metadata.Annotations[AdoptedAnnotation] == "true" {
		return nil
	}
	return k.Call(ctx, http.MethodPatch, k.Jobs()+"/"+name, map[string]any{"metadata": map[string]any{"annotations": map[string]string{AdoptedAnnotation: "true"}}}, nil)
}

func (k *Client) BuildLog(ctx context.Context, job string) string {
	var pods WorkerPodList
	if k.Call(ctx, "GET", k.Core("pods")+"?labelSelector="+url.QueryEscape("job-name="+job), nil, &pods) != nil || len(pods.Items) == 0 {
		return "Builder log unavailable"
	}
	var logs []string
	for _, container := range []string{"builder-build", "builder-pack"} {
		var text string
		route := k.Core("pods") + "/" + pods.Items[0].Metadata.Name + "/log?container=" + url.QueryEscape(container) + "&limitBytes=16384"
		if k.Call(ctx, "GET", route, nil, &text) == nil && strings.TrimSpace(text) != "" {
			logs = append(logs, text)
		}
	}
	if len(logs) == 0 {
		return "Builder log unavailable"
	}
	return workerartifact.SafeWorkerLog(strings.Join(logs, "\n"))
}
