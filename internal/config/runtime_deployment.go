package config

import (
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
	metavalidation "k8s.io/apimachinery/pkg/api/validation"
	kvalidation "k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

// RuntimePoolDeployment enables manifest generation for one serving pool.
type RuntimePoolDeployment struct {
	Image                string               `json:"image,omitempty"`
	Replicas             int                  `json:"replicas,omitempty"`
	PodLabels            map[string]string    `json:"pod_labels,omitempty"`
	ConfigMapAnnotations map[string]string    `json:"config_map_annotations,omitempty"`
	NodeSelector         map[string]string    `json:"node_selector,omitempty"`
	Tolerations          []BuilderToleration  `json:"tolerations,omitempty"`
	Resources            BuilderResources     `json:"resources,omitempty"`
	SecretName           string               `json:"secret_name,omitempty"`
	PeerService          string               `json:"peer_service,omitempty"`
	PodUIDAnnotation     string               `json:"pod_uid_annotation,omitempty"`
	ControlAddress       string               `json:"control_address,omitempty"`
	DNSPolicy            string               `json:"dns_policy,omitempty"`
	DNSConfig            *RuntimePodDNSConfig `json:"dns_config,omitempty"`
	StorageEgressCIDRs   []string             `json:"storage_egress_cidrs,omitempty"`
}

type RuntimePodDNSConfig struct {
	Nameservers []string              `json:"nameservers,omitempty"`
	Searches    []string              `json:"searches,omitempty"`
	Options     []RuntimePodDNSOption `json:"options,omitempty"`
}

type RuntimePodDNSOption struct {
	Name  string  `json:"name"`
	Value *string `json:"value,omitempty"`
}

func (c *Config) validateRuntimeDeployments() error {
	if c.Workers == nil {
		return nil
	}

	services := make(map[string]string)
	for _, pool := range c.Workers.RuntimePools {
		if pool.StreamService() != "" {
			services[pool.StreamService()] = pool.ID
		}
	}
	for _, pool := range c.Workers.RuntimePools {
		services[pool.RuntimeService] = pool.ID
		if pool.ValidatorService != "" {
			services[pool.ValidatorService] = pool.ID
		}
	}

	for _, pool := range c.Workers.RuntimePools {
		deployment := pool.Deployment
		if deployment == nil {
			continue
		}
		if err := c.validateRuntimeDeployment(pool); err != nil {
			return fmt.Errorf("workers.runtime_pools[%s].deployment: %w", pool.ID, err)
		}
		stream := pool.StreamService()
		if owner := services[stream]; stream != "" &&
			(owner != pool.ID || stream == pool.RuntimeService || stream == pool.ValidatorService) {
			return fmt.Errorf("workers.runtime_pools[%s].deployment: stream Service name conflicts with another Service", pool.ID)
		}
		peer := deployment.PeerService
		if _, exists := services[peer]; exists && peer != pool.RuntimeService {
			return fmt.Errorf("workers.runtime_pools[%s].deployment: peer_service conflicts with another Service", pool.ID)
		}
		services[peer] = pool.ID
	}
	return nil
}

func (c *Config) validateRuntimeDeployment(pool WorkerRuntimePool) error {
	d := pool.Deployment
	if c.Deployment != nil {
		if d.Image == "" {
			d.Image = c.Deployment.RuntimeImage
		}
		if d.Replicas == 0 {
			d.Replicas = c.Deployment.RuntimeReplicas
		}
	}
	if !pinnedDeploymentImage(d.Image) {
		return fmt.Errorf("image requires an explicit tag or digest")
	}
	if d.Replicas == 0 {
		d.Replicas = 2
	}
	if d.Replicas < 1 || d.Replicas > 100 {
		return fmt.Errorf("replicas must be between 1 and 100")
	}
	if len(kvalidation.IsDNS1123Label(c.Workers.Namespace)) != 0 ||
		len(kvalidation.IsDNS1035Label(pool.RuntimeService)) != 0 ||
		len(kvalidation.IsValidLabelValue(pool.ID)) != 0 {
		return fmt.Errorf("namespace, runtime_service or pool ID is invalid for Kubernetes")
	}
	if !validKubernetesLabels(d.PodLabels) {
		return fmt.Errorf("invalid pod_labels")
	}
	if errs := metavalidation.ValidateAnnotations(d.ConfigMapAnnotations, field.NewPath("config_map_annotations")); len(errs) > 0 {
		return fmt.Errorf("invalid config_map_annotations: %v", errs.ToAggregate())
	}
	for _, reserved := range []string{"app", "pool", "role", "app.kubernetes.io/part-of"} {
		if _, found := d.PodLabels[reserved]; found {
			return fmt.Errorf("pod_labels key %q is reserved", reserved)
		}
	}
	if !validKubernetesLabels(d.NodeSelector) {
		return fmt.Errorf("invalid node_selector")
	}
	for _, toleration := range d.Tolerations {
		if !validKubernetesToleration(toleration) {
			return fmt.Errorf("invalid tolerations")
		}
	}
	if d.SecretName == "" {
		d.SecretName = pool.RuntimeService
	}
	if len(kvalidation.IsDNS1123Subdomain(d.SecretName)) != 0 {
		return fmt.Errorf("invalid secret_name")
	}
	if d.PeerService == "" {
		d.PeerService = pool.DefaultPeerService()
	}
	if len(kvalidation.IsDNS1035Label(d.PeerService)) != 0 {
		return fmt.Errorf("invalid peer_service")
	}
	if d.PodUIDAnnotation != "" && len(kvalidation.IsQualifiedName(d.PodUIDAnnotation)) != 0 {
		return fmt.Errorf("invalid pod_uid_annotation")
	}
	if d.ControlAddress != "" {
		if c.Workers.RuntimeAPIURL != "" {
			return fmt.Errorf("control_address cannot be combined with workers.runtime_api_url")
		}
		host, port, err := net.SplitHostPort(d.ControlAddress)
		number, portErr := strconv.Atoi(port)
		if err != nil || portErr != nil || number < 1 || number > 65535 ||
			(net.ParseIP(host) == nil && len(kvalidation.IsDNS1123Subdomain(host)) != 0) {
			return fmt.Errorf("control_address must be a host and numeric port")
		}
	}
	if err := d.validateDNS(); err != nil {
		return err
	}
	for _, cidr := range d.StorageEgressCIDRs {
		if _, err := netip.ParsePrefix(cidr); err != nil {
			return fmt.Errorf("invalid storage_egress_cidrs")
		}
	}

	defaultResources := BuilderResources{
		Requests: BuilderResourceValues{CPU: "4", Memory: "8Gi"},
		Limits:   BuilderResourceValues{CPU: "4", Memory: "8Gi"},
	}
	d.Resources = d.Resources.WithDefaults(defaultResources)
	for name, value := range d.Resources.Requests.Map() {
		limit := d.Resources.Limits.Map()[name]
		if value == "" && limit == "" {
			continue
		}
		if limit != "" {
			quantity, err := resource.ParseQuantity(limit)
			if err != nil || quantity.Sign() <= 0 {
				return fmt.Errorf("resources.limits.%s must be a positive Kubernetes quantity", name)
			}
		}
		if value != "" {
			request, err := resource.ParseQuantity(value)
			if err != nil || request.Sign() <= 0 {
				return fmt.Errorf("resources.requests.%s must be a positive Kubernetes quantity", name)
			}
			if limitQuantity, err := resource.ParseQuantity(limit); err == nil && request.Cmp(limitQuantity) > 0 {
				return fmt.Errorf("resources.requests.%s exceeds limit", name)
			}
		}
	}
	return nil
}

func (d *RuntimePoolDeployment) validateDNS() error {
	switch d.DNSPolicy {
	case "", "ClusterFirst", "ClusterFirstWithHostNet", "Default":
	case "None":
		if d.DNSConfig == nil || len(d.DNSConfig.Nameservers) == 0 {
			return fmt.Errorf("dns_policy None requires dns_config.nameservers")
		}
	default:
		return fmt.Errorf("invalid dns_policy")
	}
	if d.DNSConfig == nil {
		return nil
	}
	dns := d.DNSConfig
	if len(dns.Nameservers) > 3 || len(dns.Searches) > 32 || len(strings.Join(dns.Searches, " ")) > 2048 {
		return fmt.Errorf("dns_config exceeds Kubernetes nameserver or search domain limits")
	}
	for _, nameserver := range dns.Nameservers {
		ip, err := netip.ParseAddr(nameserver)
		if err != nil || ip.Zone() != "" {
			return fmt.Errorf("invalid dns_config.nameservers IP address")
		}
	}
	for _, search := range dns.Searches {
		if search != "." && len(kvalidation.IsDNS1123Subdomain(strings.TrimSuffix(search, "."))) != 0 {
			return fmt.Errorf("invalid dns_config.searches domain")
		}
	}
	for _, option := range dns.Options {
		if strings.TrimSpace(option.Name) == "" {
			return fmt.Errorf("dns_config.options requires a name")
		}
	}
	return nil
}
