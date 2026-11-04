package config

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
)

// DeploymentConfig is infrastructure input in the same main YAML. Rendering is
// explicit; loading this section never contacts or changes a cluster.
type DeploymentConfig struct {
	ConsolePrefix     string            `json:"console_prefix,omitempty"`
	ConsoleGateway    string            `json:"console_gateway,omitempty"`
	ControlImage      string            `json:"control_image"`
	RuntimeImage      string            `json:"runtime_image"`
	ConsoleImage      string            `json:"console_image"`
	ConsoleOrigin     string            `json:"console_origin"`
	DNSResolver       string            `json:"dns_resolver"`
	ControlReplicas   int               `json:"control_replicas"`
	RuntimeReplicas   int               `json:"runtime_replicas"`
	ValidatorReplicas int               `json:"validator_replicas"`
	RuntimeClass      string            `json:"runtime_class,omitempty"`
	ImagePullSecrets  []string          `json:"image_pull_secrets,omitempty"`
	Network           DeploymentNetwork `json:"network"`
}

type DeploymentNetwork struct {
	Control []DeploymentEgress `json:"control"`
	Runtime []DeploymentEgress `json:"runtime"`
	Builder []DeploymentEgress `json:"builder"`
	Console []DeploymentEgress `json:"console"`
}

// PublicHTTPS is an explicit portable-policy tradeoff for dynamic cloud IPs.
// Private destinations always require CIDRs. Nothing resolves DNS at render time.
type DeploymentEgress struct {
	CIDRs       []string `json:"cidrs,omitempty"`
	Ports       []int    `json:"ports,omitempty"`
	PublicHTTPS bool     `json:"public_https,omitempty"`
}

func (d *DeploymentConfig) validate() error {
	if d == nil {
		return nil
	}
	for _, image := range []string{d.ControlImage, d.RuntimeImage, d.ConsoleImage} {
		last := image[strings.LastIndex(image, "/")+1:]
		if image == "" || strings.ContainsAny(image, " \t\r\n$") || strings.HasSuffix(image, ":latest") || !strings.Contains(last, ":") {
			return fmt.Errorf("deployment images require explicit tags or digests")
		}
	}
	u, err := url.Parse(d.ConsoleOrigin)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil ||
		u.RawQuery != "" || u.Fragment != "" || u.Path != "" || strings.ContainsAny(u.Host, "'\";${} ") {
		return fmt.Errorf("deployment console_origin must be a fixed HTTPS origin")
	}
	if d.ConsolePrefix != "" {
		if !regexp.MustCompile(`^[a-zA-Z0-9/_-]+$`).MatchString(d.ConsolePrefix) || strings.HasPrefix(d.ConsolePrefix, "/") || strings.HasSuffix(d.ConsolePrefix, "/") || strings.Contains(d.ConsolePrefix, "//") {
			return fmt.Errorf("deployment console_prefix must be an object key prefix without leading/trailing slash")
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
	for _, rules := range [][]DeploymentEgress{
		d.Network.Control,
		d.Network.Runtime,
		d.Network.Builder,
		d.Network.Console,
	} {
		if len(rules) == 0 || len(rules) > 64 {
			return fmt.Errorf("each deployment component requires explicit network egress")
		}
		for _, rule := range rules {
			if rule.PublicHTTPS {
				if len(rule.CIDRs) != 0 || len(rule.Ports) != 0 {
					return fmt.Errorf("public_https does not accept CIDRs or ports")
				}
				continue
			}
			if len(rule.CIDRs) == 0 || len(rule.Ports) == 0 {
				return fmt.Errorf("deployment egress requires CIDRs and ports")
			}
			for _, value := range rule.CIDRs {
				_, block, err := net.ParseCIDR(value)
				if err != nil {
					return fmt.Errorf("invalid deployment egress CIDR")
				}
				ones, _ := block.Mask.Size()
				if ones == 0 {
					return fmt.Errorf("use public_https for public cloud destinations; unrestricted egress is not accepted")
				}
			}
			for _, port := range rule.Ports {
				if port < 1 || port > 65535 {
					return fmt.Errorf("invalid deployment egress port")
				}
			}
		}
	}
	return nil
}
