package config

import (
	"fmt"
	"net"
	"regexp"
	"strings"
)

// DeploymentConfig is infrastructure input in the same main YAML. Rendering is
// explicit; loading this section never contacts or changes a cluster.
type DeploymentConfig struct {
	ConsoleGateway    string   `json:"console_gateway,omitempty"`
	ControlImage      string   `json:"control_image"`
	RuntimeImage      string   `json:"runtime_image"`
	DNSResolver       string   `json:"dns_resolver"`
	ControlReplicas   int      `json:"control_replicas"`
	RuntimeReplicas   int      `json:"runtime_replicas"`
	ValidatorReplicas int      `json:"validator_replicas"`
	RuntimeClass      string   `json:"runtime_class,omitempty"`
	ImagePullSecrets  []string `json:"image_pull_secrets,omitempty"`
}

func (d *DeploymentConfig) validate() error {
	if d == nil {
		return nil
	}
	for _, image := range []string{d.ControlImage, d.RuntimeImage} {
		if !pinnedDeploymentImage(image) {
			return fmt.Errorf("deployment images require explicit tags or digests")
		}
	}
	if net.ParseIP(d.DNSResolver) == nil {
		return fmt.Errorf("deployment dns_resolver must be the explicit cluster DNS IP")
	}
	if d.ControlReplicas == 0 {
		d.ControlReplicas = 1
	}
	if d.RuntimeReplicas == 0 {
		d.RuntimeReplicas = 2
	}
	if d.ValidatorReplicas == 0 {
		d.ValidatorReplicas = 1
	}
	for _, count := range []int{d.ControlReplicas, d.RuntimeReplicas, d.ValidatorReplicas} {
		if count < 1 || count > 100 {
			return fmt.Errorf("deployment replica count must be between 1 and 100")
		}
	}
	name := regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	for _, value := range append(append([]string{}, d.ImagePullSecrets...), d.RuntimeClass) {
		if value != "" && (!name.MatchString(value) || len(value) > 63) {
			return fmt.Errorf("invalid deployment Kubernetes resource name")
		}
	}

	return nil
}

func pinnedDeploymentImage(image string) bool {
	last := image[strings.LastIndex(image, "/")+1:]
	name, tag, found := strings.Cut(last, ":")
	return found && name != "" && tag != "" && !strings.ContainsAny(image, " \t\r\n$") && !strings.HasSuffix(image, ":latest")
}
