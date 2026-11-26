package config

import (
	"fmt"

	"k8s.io/apimachinery/pkg/api/resource"
	kvalidation "k8s.io/apimachinery/pkg/util/validation"
)

type BuilderResourceValues struct {
	CPU              string `json:"cpu,omitempty"`
	Memory           string `json:"memory,omitempty"`
	EphemeralStorage string `json:"ephemeral-storage,omitempty"`
}

type BuilderResources struct {
	Requests BuilderResourceValues `json:"requests,omitempty"`
	Limits   BuilderResourceValues `json:"limits,omitempty"`
}

type BuilderToleration struct {
	Key               string `json:"key,omitempty"`
	Operator          string `json:"operator,omitempty"`
	Value             string `json:"value,omitempty"`
	Effect            string `json:"effect,omitempty"`
	TolerationSeconds *int64 `json:"tolerationSeconds,omitempty"`
}

func DefaultBuilderBuildResources() BuilderResources {
	return BuilderResources{Requests: BuilderResourceValues{"100m", "256Mi", "512Mi"}, Limits: BuilderResourceValues{"2", "2Gi", "3Gi"}}
}

func DefaultBuilderPackResources() BuilderResources {
	return BuilderResources{Requests: BuilderResourceValues{"50m", "128Mi", "64Mi"}, Limits: BuilderResourceValues{"1", "512Mi", "512Mi"}}
}

func (v BuilderResourceValues) Map() map[string]string {
	return map[string]string{"cpu": v.CPU, "memory": v.Memory, "ephemeral-storage": v.EphemeralStorage}
}

func (r BuilderResources) WithDefaults(d BuilderResources) BuilderResources {
	fill := func(v *BuilderResourceValues, defaults BuilderResourceValues) {
		if v.CPU == "" {
			v.CPU = defaults.CPU
		}
		if v.Memory == "" {
			v.Memory = defaults.Memory
		}
		if v.EphemeralStorage == "" {
			v.EphemeralStorage = defaults.EphemeralStorage
		}
	}
	fill(&r.Requests, d.Requests)
	fill(&r.Limits, d.Limits)
	return r
}

func (c *WorkersConfig) validateBuilderJob() error {
	if c.BuilderInputMode != "" && c.BuilderInputMode != "secret" && c.BuilderInputMode != "env" {
		return fmt.Errorf("workers.builder_input_mode must be secret or env")
	}
	c.BuilderBuildResources = c.BuilderBuildResources.WithDefaults(DefaultBuilderBuildResources())
	c.BuilderPackResources = c.BuilderPackResources.WithDefaults(DefaultBuilderPackResources())
	for name, resources := range map[string]BuilderResources{"builder_build_resources": c.BuilderBuildResources, "builder_pack_resources": c.BuilderPackResources} {
		for key, value := range resources.Requests.Map() {
			request, err := resource.ParseQuantity(value)
			if err != nil || request.Sign() <= 0 {
				return fmt.Errorf("workers.%s.requests.%s must be a positive Kubernetes quantity", name, key)
			}
			limit, err := resource.ParseQuantity(resources.Limits.Map()[key])
			if err != nil || limit.Sign() <= 0 {
				return fmt.Errorf("workers.%s.limits.%s must be a positive Kubernetes quantity", name, key)
			}
			if request.Cmp(limit) > 0 {
				return fmt.Errorf("workers.%s.requests.%s exceeds limit", name, key)
			}
		}
	}
	if !validKubernetesLabels(c.BuilderNodeSelector) {
		return fmt.Errorf("invalid workers.builder_node_selector")
	}
	for _, toleration := range c.BuilderTolerations {
		if !validKubernetesToleration(toleration) {
			return fmt.Errorf("invalid workers.builder_tolerations")
		}
	}
	return nil
}

func validKubernetesLabels(labels map[string]string) bool {
	for key, value := range labels {
		if len(kvalidation.IsQualifiedName(key)) != 0 || len(kvalidation.IsValidLabelValue(value)) != 0 {
			return false
		}
	}
	return true
}

func validKubernetesToleration(t BuilderToleration) bool {
	if (t.Key != "" && len(kvalidation.IsQualifiedName(t.Key)) != 0) ||
		(t.Operator != "" && t.Operator != "Equal" && t.Operator != "Exists") ||
		(t.Key == "" && t.Operator != "Exists") ||
		(t.Operator == "Exists" && t.Value != "") ||
		len(kvalidation.IsValidLabelValue(t.Value)) != 0 {
		return false
	}
	switch t.Effect {
	case "", "NoSchedule", "PreferNoSchedule", "NoExecute":
		return t.TolerationSeconds == nil || (t.Effect == "NoExecute" && *t.TolerationSeconds >= 0)
	default:
		return false
	}
}
