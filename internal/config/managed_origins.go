package config

import (
	"fmt"
	"regexp"
	"slices"
)

// ManagedOrigin maps an origin DNS name to a Kubernetes Service. Organizations
// can refer to an entry only on the suffixes explicitly allowed by operators.
type ManagedOrigin struct {
	Host          string   `json:"host"`
	Kind          string   `json:"kind"`
	Label         string   `json:"label,omitempty"`
	Service       string   `json:"service"`
	Namespace     string   `json:"namespace,omitempty"`
	Port          int      `json:"port"`
	HostHeader    string   `json:"host_header,omitempty"`
	Organizations []string `json:"organizations"`
	Suffixes      []string `json:"suffixes"`
}

var originServiceName = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,61}[a-z0-9])?$`)
var originDNS = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$`)

func (c *WorkersConfig) validateManagedOrigins() error {
	seen := map[string]bool{}
	for _, origin := range c.ManagedOrigins {
		if seen[origin.Host] || !originDNS.MatchString(origin.Host) || len(origin.Host) > 253 {
			return fmt.Errorf("invalid or duplicate managed origin hostname %q", origin.Host)
		}
		seen[origin.Host] = true
		if origin.Kind != "static" && origin.Kind != "http" {
			return fmt.Errorf("managed origin %s kind must be static or http", origin.Host)
		}
		if !originServiceName.MatchString(origin.Service) || (origin.Namespace != "" && !originServiceName.MatchString(origin.Namespace)) ||
			origin.Port < 1 || origin.Port > 65535 {
			return fmt.Errorf("managed origin %s requires a valid Kubernetes Service and port", origin.Host)
		}
		if origin.HostHeader != "" && (!originDNS.MatchString(origin.HostHeader) || len(origin.HostHeader) > 253) {
			return fmt.Errorf("managed origin %s has an invalid Host override", origin.Host)
		}
		if len(origin.Organizations) == 0 || len(origin.Suffixes) == 0 || slices.Contains(origin.Organizations, "") {
			return fmt.Errorf("managed origin %s requires explicit organizations and suffixes", origin.Host)
		}
		for _, id := range origin.Suffixes {
			suffix, ok := c.Domain(id)
			if !ok || !suffix.Allows(WorkerDomainApplication) {
				return fmt.Errorf("managed origin %s requires an application suffix: %s", origin.Host, id)
			}
		}
	}
	return nil
}

func (c *WorkersConfig) ManagedOriginFor(org, suffix, host string) (ManagedOrigin, bool) {
	for _, origin := range c.ManagedOrigins {
		if origin.Host == host && slices.Contains(origin.Organizations, org) && slices.Contains(origin.Suffixes, suffix) {
			return origin, true
		}
	}
	return ManagedOrigin{}, false
}
