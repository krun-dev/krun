package deployment

import (
	"net"

	"github.com/krun-dev/krun/internal/config"
)

func tcp(port int) map[string]any { return map[string]any{"protocol": "TCP", "port": port} }
func peer(app string) map[string]any {
	return map[string]any{"podSelector": map[string]any{"matchLabels": map[string]string{"app": app}}}
}
func podRole(role string) map[string]any {
	return map[string]any{
		"podSelector": map[string]any{"matchLabels": map[string]string{"role": role}},
	}
}
func gatewayPeer(gateway config.WorkerGateway) map[string]any {
	return map[string]any{
		"namespaceSelector": map[string]any{
			"matchLabels": map[string]string{
				"kubernetes.io/metadata.name": gateway.Namespace,
			},
		},
		"podSelector": map[string]any{
			"matchLabels": map[string]string{
				"gateway.networking.k8s.io/gateway-name": gateway.Name,
			},
		},
	}
}
func ingress(peers []any, ports ...int) map[string]any {
	values := []any{}
	for _, port := range ports {
		values = append(values, tcp(port))
	}
	return map[string]any{"from": peers, "ports": values}
}
func egress(peers []any, ports ...int) map[string]any {
	values := []any{}
	for _, port := range ports {
		values = append(values, tcp(port))
	}
	return map[string]any{"to": peers, "ports": values}
}
func configuredEgress(rules []config.DeploymentEgress) []any {
	out := []any{}
	for _, rule := range rules {
		peers := []any{}
		ports := rule.Ports
		if rule.PublicHTTPS {
			peers = append(peers, map[string]any{
				"ipBlock": map[string]any{
					"cidr": "0.0.0.0/0",
					"except": []string{
						"0.0.0.0/8",
						"10.0.0.0/8",
						"100.64.0.0/10",
						"127.0.0.0/8",
						"169.254.0.0/16",
						"172.16.0.0/12",
						"192.168.0.0/16",
						"224.0.0.0/4",
					},
				},
			})
			ports = []int{443}
		} else {
			for _, cidr := range rule.CIDRs {
				peers = append(peers, map[string]any{"ipBlock": map[string]string{"cidr": cidr}})
			}
		}
		out = append(out, egress(peers, ports...))
	}
	return out
}
func (r *renderer) policy(name string, selector map[string]string, in, out []any) {
	r.add("networking.k8s.io/v1", "NetworkPolicy", name, map[string]any{
		"spec": map[string]any{
			"podSelector": map[string]any{"matchLabels": selector},
			"policyTypes": []string{
				"Ingress",
				"Egress",
			},
			"ingress": in,
			"egress":  out,
		},
	})
}
func (r *renderer) networkPolicies() {
	r.policy("krun-default-deny", map[string]string{}, []any{}, []any{})
	suffix := "/32"
	if net.ParseIP(r.cfg.Deployment.DNSResolver).To4() == nil {
		suffix = "/128"
	}
	dnsPeers := []any{
		map[string]any{"ipBlock": map[string]string{"cidr": r.cfg.Deployment.DNSResolver + suffix}},
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
	}
	r.add("networking.k8s.io/v1", "NetworkPolicy", "krun-dns", map[string]any{
		"spec": map[string]any{
			"podSelector": map[string]any{},
			"policyTypes": []string{"Egress"},
			"egress": []any{
				map[string]any{
					"to": dnsPeers,
					"ports": []any{
						tcp(53),
						map[string]any{
							"protocol": "UDP",
							"port":     53,
						},
					},
				},
			},
		},
	})
	controlOut := configuredEgress(r.cfg.Deployment.Network.Control)
	controlOut = append(controlOut, egress([]any{podRole("krun-runtime"), podRole("krun-validator")}, 8083))
	for _, gateway := range r.cfg.Workers.Gateways {
		controlOut = append(controlOut, egress([]any{gatewayPeer(gateway)}, gatewayPort(gateway)))
	}
	r.policy("krun-control", map[string]string{"app": "krun-control"}, []any{
		ingress([]any{gatewayPeer(r.gateway)}, integerPort(r.cfg.Listen)),
		ingress([]any{podRole("krun-runtime"), podRole("krun-validator")}, integerPort(r.cfg.InternalListen)),
	}, controlOut)
	for _, pool := range r.cfg.Workers.RuntimePools {
		r.pool = pool
		for _, validator := range []bool{false, true} {
			role := "krun-runtime"
			name := r.pool.RuntimeService
			incoming := []any{ingress([]any{peer("krun-control")}, 8083)}
			if validator {
				role = "krun-validator"
				name = r.pool.ValidatorService
			} else {
				if peers := r.poolGatewayPeers(pool.ID); len(peers) > 0 {
					incoming = append(incoming, ingress(peers, 8081))
				}
			}
			outgoing := configuredEgress(r.cfg.Deployment.Network.Runtime)
			outgoing = append(outgoing, egress([]any{peer("krun-control")}, integerPort(r.cfg.InternalListen)))
			r.policy(name, map[string]string{"role": role, "pool": r.pool.ID}, incoming, outgoing)
		}
	}
	r.policy("krun-builder", map[string]string{"app": "krun-builder"}, []any{}, configuredEgress(r.cfg.Deployment.Network.Builder))
	r.policy("krun-console", map[string]string{"app": "krun-console"}, []any{
		ingress([]any{
			gatewayPeer(r.gateway),
		}, 8080),
	}, configuredEgress(r.cfg.Deployment.Network.Console))
}

// Only gateways selected by profiles that can use this pool may enter it.
func (r *renderer) poolGatewayPeers(poolID string) []any {
	ids := map[string]bool{}
	for _, profile := range r.cfg.Workers.NetworkProfiles {
		for _, pool := range r.cfg.Workers.PoolsForProfile(profile.ID) {
			if pool.ID != poolID {
				continue
			}
			for _, domain := range r.cfg.Workers.DomainsForProfile(profile) {
				ids[domain.Gateway] = true
			}
		}
	}
	peers := []any{}
	for _, gateway := range r.cfg.Workers.Gateways {
		if ids[gateway.ID] {
			peers = append(peers, gatewayPeer(gateway))
		}
	}
	return peers
}
