package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	slices "slices"
	"strings"

	workerartifact "github.com/krun-dev/krun/internal/artifact"
	domain "github.com/krun-dev/krun/internal/domain"
	securetoken "github.com/krun-dev/krun/internal/securetoken"
)

// selectPoolForService uses rendezvous hashing so adding or removing a pool
// only moves the services whose winner changes. The result is persisted as a
// placement; this function is also useful for repairing a missing placement.
func (c *WorkersConfig) SelectPoolForService(profileID, serviceID string) (WorkerRuntimePool, bool) {
	pools := c.PoolsForProfile(profileID)
	if len(pools) == 0 {
		return WorkerRuntimePool{}, false
	}
	best, bestScore := pools[0], ""
	for _, pool := range pools {
		score := workerartifact.BytesDigest([]byte(profileID + "\x00" + serviceID + "\x00" + pool.ID))
		if score > bestScore {
			best, bestScore = pool, score
		}
	}
	return best, true
}

// validateCatalog normalises and checks the Gateway and domain lists. Gateways
// come first so domains can reference them by id.
func (c *WorkersConfig) ValidateCatalog(dns *regexp.Regexp) error {
	if len(c.Gateways) == 0 {
		return fmt.Errorf("Workers gateways required")
	}
	seenGateway := map[string]bool{}
	for i := range c.Gateways {
		g := &c.Gateways[i]
		if g.ID == "" {
			return fmt.Errorf("Workers gateway id required")
		}
		if seenGateway[g.ID] {
			return fmt.Errorf("duplicate Workers gateway id: %s", g.ID)
		}
		seenGateway[g.ID] = true
		if g.Namespace == "" {
			g.Namespace = "kgateway-system"
		}
		if g.Section == "" {
			g.Section = g.ID
		}
		for _, v := range []string{g.Name, g.Namespace, g.Section} {
			if !dns.MatchString(v) {
				return fmt.Errorf("invalid Workers gateway %s", g.ID)
			}
		}
		if g.Base != "" {
			u, e := url.Parse(g.Base)
			if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
				return fmt.Errorf("invalid Workers gateway base for %s", g.ID)
			}
			g.Base = strings.TrimSuffix(g.Base, "/")
		}
	}
	if len(c.Domains) == 0 {
		return fmt.Errorf("Workers domains required")
	}
	seenDomain, seenID := map[string]bool{}, map[string]bool{}
	for i := range c.Domains {
		d := &c.Domains[i]
		if d.ID == "" {
			return fmt.Errorf("Workers domain id required")
		}
		if seenID[d.ID] {
			return fmt.Errorf("duplicate Workers domain id: %s", d.ID)
		}
		seenID[d.ID] = true
		if !dns.MatchString(d.Domain) {
			return fmt.Errorf("invalid Workers domain: %s", d.Domain)
		}
		if d.Domain != strings.ToLower(d.Domain) {
			return fmt.Errorf("invalid Workers domain: %s", d.Domain)
		}
		if seenDomain[d.Domain] {
			return fmt.Errorf("duplicate Workers domain: %s", d.Domain)
		}
		seenDomain[d.Domain] = true
		if !seenGateway[d.Gateway] {
			return fmt.Errorf("Workers domain %s references unknown gateway %q", d.ID, d.Gateway)
		}
		if d.Access == "" {
			d.Access = "public"
		}
		if d.Access != "public" && d.Access != "private" {
			return fmt.Errorf("Workers domain %s access must be public or private", d.ID)
		}
		if err := ValidateWorkerLabels(d.Labels); err != nil {
			return fmt.Errorf("Workers domain %s labels: %w", d.ID, err)
		}
		if d.StaticDomain == "" {
			d.StaticDomain = "static." + d.Domain
		}
		if !dns.MatchString(d.StaticDomain) {
			return fmt.Errorf("invalid Workers static domain for %s", d.ID)
		}
	}
	if len(c.Domains) == 1 && !c.Domains[0].Default {
		c.Domains[0].Default = true
	}
	return nil
}

// gateway returns the configured Gateway instance by id.
func (c *WorkersConfig) Gateway(id string) (WorkerGateway, bool) {
	if c == nil {
		return WorkerGateway{}, false
	}
	for _, g := range c.Gateways {
		if g.ID == id {
			return g, true
		}
	}
	return WorkerGateway{}, false
}

// domain returns the configured domain entry by id.
func (c *WorkersConfig) Domain(id string) (WorkerDomain, bool) {
	if c == nil {
		return WorkerDomain{}, false
	}
	for _, d := range c.Domains {
		if d.ID == id {
			return d, true
		}
	}
	return WorkerDomain{}, false
}

// domainForHost resolves which configured domain a route host belongs to. The
// longest matching suffix wins so overlapping suffixes stay unambiguous.
func (c *WorkersConfig) DomainForHost(host string) (WorkerDomain, bool) {
	if c == nil || host == "" {
		return WorkerDomain{}, false
	}
	var best WorkerDomain
	found := false
	bestLen := 0
	for _, d := range c.Domains {
		if strings.HasSuffix(host, "."+d.Domain) && len(d.Domain) > bestLen {
			best, bestLen, found = d, len(d.Domain), true
		}
	}
	return best, found
}

// defaultDomains returns the ids newly created environments start with.
func (c *WorkersConfig) DefaultDomains() []string {
	out := []string{}
	if c == nil {
		return out
	}
	for _, d := range c.Domains {
		if d.Default {
			out = append(out, d.ID)
		}
	}
	if len(out) == 0 && len(c.Domains) > 0 {
		out = append(out, c.Domains[0].ID)
	}
	return out
}

// workerSlug is the immutable, non-enumerable service label. It is always
// "w-" plus 16 hex characters, so a host can be split into slug and
// environment without ambiguity: the first '-' after position 18 starts the
// environment name.
func WorkerSlug(organization, service string) string {
	return "w-" + securetoken.Digest(organization + "/" + service)[:16]
}

// hostFor derives the route host for one environment under one domain suffix.
// Every domain of an environment shares the same label, so a service keeps one
// identity; only the DNS suffix differs, and the suffix decides which Gateway
// instance serves the host.
func HostFor(slug, env, suffix string) string {
	if slug == "" || env == "" || suffix == "" {
		return ""
	}
	return slug + "-" + env + "." + suffix
}

// gatewayBase returns the address console invoke uses for a Gateway instance.
// An explicit base (SSH tunnel) wins; otherwise the in-cluster service is used.
func (g WorkerGateway) GatewayBase() string {
	if g.Base != "" {
		return g.Base
	}
	return ""
}

func (g WorkerGateway) InClusterAddress() string {
	return g.Name + "." + g.Namespace + ".svc:8080"
}

// A service has one durable baseline ("production") plus any number of
// short-lived preview environments identified by a slug.
const WorkerBaseline = "production"

func WorkerEnvValid(v string) bool { return v == WorkerBaseline || WorkerPreviewValid(v) }

func WorkerPreviewValid(v string) bool {
	return v != WorkerBaseline && Slug.MatchString(v)
}

func WorkerTerminal(v string) bool {
	return slices.Contains([]string{"succeeded", "failed", "available", "superseded"}, v)
}

func WorkerOperationAction(kind string) string {
	if kind == "secret" {
		return "service.publish"
	}
	return "service." + kind
}

func WorkerVariablesValid(vars map[string]any) bool {
	if len(vars) > 32 {
		return false
	}
	size := 0
	re := regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]{0,127}$`)
	for k, v := range vars {
		if !re.MatchString(k) || strings.HasPrefix(k, "KRUN_") {
			return false
		}
		raw, err := json.Marshal(v)
		if err != nil {
			return false
		}
		size += len(k) + len(raw)
	}
	return size <= 16<<10
}

func (c *WorkersConfig) ServicePool(state *domain.WorkerState, service domain.WorkerService) string {
	if p, ok := domain.ActiveWorkerPlacement(state, service.Service); ok && p.NetworkProfile == service.NetworkProfile {
		return p.Pool
	}
	if p, ok := c.SelectPoolForService(service.NetworkProfile, service.Service); ok {
		return p.ID
	}
	return ""
}
