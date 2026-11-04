package devruntime

import (
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strings"

	"go.yaml.in/yaml/v3"
)

func (o renderOptions) renderNetworkPolicies(outputDirectory string, replacements *strings.Replacer) error {
	raw, err := os.ReadFile("deploy/workers-dev/networkpolicy.yaml.tmpl")
	if err != nil {
		return err
	}

	decoder := yaml.NewDecoder(strings.NewReader(replacements.Replace(string(raw))))
	policies := []any{}
	for {
		var policy map[string]any
		if err := decoder.Decode(&policy); err == io.EOF {
			break
		} else if err != nil {
			return err
		}

		spec := policy["spec"].(map[string]any)
		egress := spec["egress"].([]any)
		controlRule := egress[len(egress)-1].(map[string]any)
		controlRule["ports"] = []any{
			map[string]any{
				"protocol": "TCP",
				"port":     mustPort(o.controlPort),
			},
		}

		storageRule := o.storageEgressRule()
		spec["egress"] = []any{controlRule, storageRule}
		needsDNS := net.ParseIP(o.storageHost) == nil
		labels := spec["podSelector"].(map[string]any)["matchLabels"].(map[string]any)
		if labels["role"] == "krun-runtime" && o.workers.ObjectStorage != nil {
			for _, store := range o.workers.ObjectStorage.Stores {
				u, _ := url.Parse(store.Endpoint)
				target := o
				target.storageHost = u.Hostname()
				target.storagePort = u.Port()
				if target.storagePort == "" {
					target.storagePort = "80"
					if u.Scheme == "https" {
						target.storagePort = "443"
					}
				}
				spec["egress"] = append(spec["egress"].([]any), target.storageEgressRule())
				needsDNS = needsDNS || net.ParseIP(target.storageHost) == nil
			}
		}
		if needsDNS {
			spec["egress"] = append(spec["egress"].([]any), dnsEgressRule())
		}

		policies = append(policies, policy)
	}

	return writeResource(outputDirectory, "networkpolicy.json", resourceList(policies))
}

func dnsEgressRule() map[string]any {
	return map[string]any{
		"to": []any{
			map[string]any{
				"namespaceSelector": map[string]any{
					"matchLabels": map[string]string{
						"kubernetes.io/metadata.name": "kube-system",
					},
				},
				"podSelector": map[string]any{
					"matchLabels": map[string]string{
						"k8s-app": "kube-dns",
					},
				},
			},
		},
		"ports": []any{
			map[string]any{"protocol": "UDP", "port": 53},
			map[string]any{"protocol": "TCP", "port": 53},
		},
	}
}

func (o renderOptions) storageEgressRule() map[string]any {
	cidr := o.storageHost + "/32"
	if ip := net.ParseIP(o.storageHost); ip != nil && ip.To4() == nil {
		cidr = o.storageHost + "/128"
	}
	ipBlock := map[string]any{"cidr": cidr}
	if net.ParseIP(o.storageHost) == nil {
		// Public S3 endpoints can change addresses. Private storage uses an
		// explicit IP, keeping its destination restricted to one address.
		// The trusted entry worker validates every configuration URL before
		// signing it; untrusted Workers have no outbound network capability.
		ipBlock = map[string]any{
			"cidr": "0.0.0.0/0",
			"except": []string{
				"10.0.0.0/8",
				"172.16.0.0/12",
				"192.168.0.0/16",
				"127.0.0.0/8",
				"169.254.0.0/16",
			},
		}
	}

	return map[string]any{
		"to": []any{
			map[string]any{"ipBlock": ipBlock},
		},
		"ports": []any{
			map[string]any{"protocol": "TCP", "port": mustPort(o.storagePort)},
		},
	}
}

func mustPort(raw string) int {
	var port int
	fmt.Sscan(raw, &port)
	return port
}
