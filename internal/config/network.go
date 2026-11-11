package config

import (
	"fmt"
	"net/netip"
	slices "slices"
	"sort"

	domain "github.com/krun-dev/krun/internal/domain"
)

func NormalizeWorkerAccess(values []string) ([]string, error) {
	seen := map[string]bool{}
	for _, value := range values {
		if value != "public" && value != "private" {
			return nil, fmt.Errorf("Workers runtime pool access must contain only public or private")
		}
		seen[value] = true
	}
	out := []string{}
	for _, value := range []string{"public", "private"} {
		if seen[value] {
			out = append(out, value)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("Workers runtime pool access required")
	}
	return out, nil
}

func NormalizeWorkerEgress(pool *WorkerRuntimePool) error {
	if pool.PrivateEgress == "" {
		pool.PrivateEgress = "deny"
	}
	if pool.PublicEgress == "" {
		pool.PublicEgress = "deny"
	}
	if pool.PrivateEgress != "deny" && pool.PrivateEgress != "direct" {
		return fmt.Errorf("private_egress must be deny or direct")
	}
	if pool.PublicEgress != "deny" && pool.PublicEgress != "direct" && pool.PublicEgress != "connect-proxy" {
		return fmt.Errorf("public_egress must be deny, direct or connect-proxy")
	}
	return nil
}

func (c *WorkersConfig) validateProtectedEgressCIDRs() error {
	if len(c.ProtectedEgressCIDRs) > 64 {
		return fmt.Errorf("Workers protected_egress_cidrs allows at most 64 entries")
	}
	for i, cidr := range c.ProtectedEgressCIDRs {
		prefix, err := netip.ParsePrefix(cidr)
		if err != nil || prefix.Addr().Is4In6() {
			return fmt.Errorf("invalid Workers protected_egress_cidrs entry at index %d", i)
		}
		c.ProtectedEgressCIDRs[i] = prefix.Masked().String()
	}
	return nil
}

func (c *WorkersConfig) validateLocalDatabaseEgress() error {
	if len(c.LocalDatabaseEgress) > 64 {
		return fmt.Errorf("Workers local_database_egress allows at most 64 entries")
	}
	for i, target := range c.LocalDatabaseEgress {
		prefix, err := netip.ParsePrefix(target.CIDR)
		if err != nil || prefix.Bits() == 0 || prefix.Addr().Is4In6() {
			return fmt.Errorf("invalid Workers local_database_egress CIDR at index %d", i)
		}
		if target.Port < 1 || target.Port > 65535 {
			return fmt.Errorf("invalid Workers local_database_egress port at index %d", i)
		}
		c.LocalDatabaseEgress[i].CIDR = prefix.Masked().String()
	}
	return nil
}

func ValidateWorkerLabels(labels map[string]string) error {
	if len(labels) > 32 {
		return fmt.Errorf("at most 32 labels are allowed")
	}
	for key, value := range labels {
		if key == "" || value == "" || len(key) > 64 || len(value) > 128 {
			return fmt.Errorf("labels require non-empty keys and values within limits")
		}
	}
	return nil
}

func LabelsMatch(labels, selector map[string]string) bool {
	for key, value := range selector {
		if labels[key] != value {
			return false
		}
	}
	return true
}

func (c *WorkersConfig) ValidateNetworkProfiles() error {
	if len(c.NetworkProfiles) == 0 {
		return fmt.Errorf("Workers network_profiles required")
	}
	seen := map[string]bool{}
	defaults := 0
	for i := range c.NetworkProfiles {
		profile := &c.NetworkProfiles[i]
		if !Slug.MatchString(profile.ID) || seen[profile.ID] {
			return fmt.Errorf("invalid or duplicate Workers network profile id")
		}
		seen[profile.ID] = true
		if profile.Name == "" {
			profile.Name = profile.ID
		}
		access, err := NormalizeWorkerAccess(profile.Requirements.Ingress)
		if err != nil {
			return fmt.Errorf("Workers network profile %s: %w", profile.ID, err)
		}
		profile.Requirements.Ingress = access
		if profile.Requirements.PrivateEgress == "" {
			profile.Requirements.PrivateEgress = "deny"
		}
		if profile.Requirements.PublicEgress == "" {
			profile.Requirements.PublicEgress = "deny"
		}
		probe := WorkerRuntimePool{Access: access, PrivateEgress: profile.Requirements.PrivateEgress, PublicEgress: profile.Requirements.PublicEgress}
		if err := NormalizeWorkerEgress(&probe); err != nil || probe.PrivateEgress != profile.Requirements.PrivateEgress || probe.PublicEgress != profile.Requirements.PublicEgress {
			return fmt.Errorf("invalid Workers network profile egress for %s", profile.ID)
		}
		if err := ValidateWorkerLabels(profile.DomainSelector); err != nil {
			return fmt.Errorf("Workers network profile %s domain selector: %w", profile.ID, err)
		}
		if err := ValidateWorkerLabels(profile.PoolSelector); err != nil {
			return fmt.Errorf("Workers network profile %s pool selector: %w", profile.ID, err)
		}
		if profile.Approval == "" {
			profile.Approval = "none"
		}
		if profile.Approval != "none" && profile.Approval != "required" {
			return fmt.Errorf("Workers network profile approval must be none or required")
		}
		if profile.Visibility == "" {
			profile.Visibility = "public"
		}
		if profile.Visibility != "public" && profile.Visibility != "granted-only" && profile.Visibility != "assigned-only" {
			return fmt.Errorf("invalid Workers network profile visibility")
		}
		if len(c.DomainsForProfile(*profile)) == 0 {
			return fmt.Errorf("Workers network profile %s selects no domains", profile.ID)
		}
		if len(c.PoolsForProfile(profile.ID)) == 0 {
			return fmt.Errorf("Workers network profile %s must resolve to at least one runtime pool", profile.ID)
		}
		if profile.Default {
			defaults++
			if profile.Approval != "none" || profile.Visibility != "public" {
				return fmt.Errorf("Workers default network profile must be public and require no approval")
			}
		}
	}
	if defaults == 0 && len(c.NetworkProfiles) == 1 {
		c.NetworkProfiles[0].Default = true
		defaults = 1
	}
	if defaults != 1 {
		return fmt.Errorf("Workers network profiles require exactly one default")
	}
	return nil
}

func WorkerAccessContains(available, requested []string) bool {
	for _, wanted := range requested {
		found := false
		for _, value := range available {
			if value == wanted {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func RuntimePoolHasAccess(pool WorkerRuntimePool, access string) bool {
	for _, value := range pool.Access {
		if value == access {
			return true
		}
	}
	return false
}

func (c *WorkersConfig) ConfiguredNetworkProfiles() []WorkerNetworkProfile {
	return c.NetworkProfiles
}

func (c *WorkersConfig) NetworkProfile(id string) (WorkerNetworkProfile, bool) {
	for _, profile := range c.ConfiguredNetworkProfiles() {
		if profile.ID == id {
			return profile, true
		}
	}
	return WorkerNetworkProfile{}, false
}

func (c *WorkersConfig) DefaultNetworkProfile() WorkerNetworkProfile {
	for _, profile := range c.ConfiguredNetworkProfiles() {
		if profile.Default {
			return profile
		}
	}
	if len(c.NetworkProfiles) > 0 {
		return c.NetworkProfiles[0]
	}
	return WorkerNetworkProfile{}
}

// normalizeWorkerNetwork resolves saved product intent into its allowed
// domain set. Physical placement is a separate durable entity. Keep this
// independent from HTTP/API state so every projection uses the same defaults.
func (c *WorkersConfig) NormalizeWorkerNetwork(ws domain.Worker) domain.Worker {
	profile, ok := c.NetworkProfile(ws.NetworkProfile)
	if !ok {
		profile = c.DefaultNetworkProfile()
		ws.NetworkProfile = profile.ID
	}
	eligible := map[string]bool{}
	for _, domain := range c.DomainsForProfile(profile) {
		eligible[domain.ID] = true
	}
	selected := make([]string, 0, len(ws.Domains))
	for _, id := range ws.Domains {
		if eligible[id] {
			selected = append(selected, id)
		}
	}
	if len(selected) == 0 {
		selected = c.DefaultDomainsForProfile(profile)
	}
	ws.Domains = selected
	return ws
}

func (c *WorkersConfig) DomainsForProfile(profile WorkerNetworkProfile) []WorkerDomain {
	out := []WorkerDomain{}
	for _, domain := range c.Domains {
		if !slices.Contains(profile.Requirements.Ingress, domain.Access) || !LabelsMatch(domain.Labels, profile.DomainSelector) {
			continue
		}
		out = append(out, domain)
	}
	return out
}

func (c *WorkersConfig) DefaultDomainsForProfile(profile WorkerNetworkProfile) []string {
	eligible := c.DomainsForProfile(profile)
	out := []string{}
	for _, domain := range eligible {
		if domain.Default {
			out = append(out, domain.ID)
		}
	}
	if len(out) == 0 && len(eligible) > 0 {
		out = append(out, eligible[0].ID)
	}
	return out
}

func (c *WorkersConfig) PoolsForProfile(profileID string) []WorkerRuntimePool {
	profile, ok := c.NetworkProfile(profileID)
	if !ok {
		return nil
	}
	matched := []WorkerRuntimePool{}
	for _, pool := range c.ConfiguredRuntimePools() {
		if !WorkerAccessContains(pool.Access, profile.Requirements.Ingress) ||
			pool.PrivateEgress != profile.Requirements.PrivateEgress ||
			pool.PublicEgress != profile.Requirements.PublicEgress ||
			!LabelsMatch(pool.Labels, profile.PoolSelector) {
			continue
		}
		matched = append(matched, pool)
	}
	sort.Slice(matched, func(i, j int) bool { return matched[i].ID < matched[j].ID })
	return matched
}

func (c *WorkersConfig) PoolAllowedForProfile(profileID, poolID string) bool {
	for _, pool := range c.PoolsForProfile(profileID) {
		if pool.ID == poolID {
			return true
		}
	}
	return false
}

// DomainRuntimePool resolves the sole backend of a domain wildcard. A flat
// hostname cannot choose between independent pools behind the same wildcard.
func (c *WorkersConfig) DomainRuntimePool(domainID string) (WorkerRuntimePool, error) {
	owners := map[string]WorkerRuntimePool{}
	for _, profile := range c.NetworkProfiles {
		for _, d := range c.DomainsForProfile(profile) {
			if d.ID != domainID {
				continue
			}
			for _, pool := range c.PoolsForProfile(profile.ID) {
				owners[pool.ID] = pool
			}
		}
	}
	if len(owners) != 1 {
		return WorkerRuntimePool{}, fmt.Errorf("domain %s must select exactly one runtime pool through network profiles; scale replicas within that pool", domainID)
	}
	for _, pool := range owners {
		return pool, nil
	}
	return WorkerRuntimePool{}, fmt.Errorf("domain %s has no runtime pool", domainID)
}
