package config

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

func (c *WorkersConfig) ValidateRuntimePools(dns *regexp.Regexp) error {
	if len(c.RuntimePools) == 0 {
		return fmt.Errorf("Workers runtime_pools required")
	}
	seenIDs, seenServices := map[string]bool{}, map[string]bool{}
	seenRuntimeTokens, seenManagementTokens := map[string]bool{}, map[string]bool{}
	seenCredentials := map[string]bool{}
	domainAccess := map[string]bool{}
	for _, domain := range c.Domains {
		domainAccess[domain.Access] = true
	}
	defaults := 0
	for i := range c.RuntimePools {
		pool := &c.RuntimePools[i]
		if pool.ValidatorToken == "" {
			mac := hmac.New(sha256.New, []byte(pool.RuntimeToken))
			mac.Write([]byte("krun/validator/" + pool.ID))
			pool.ValidatorToken = base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
		}
		if !Slug.MatchString(pool.ID) || !dns.MatchString(pool.RuntimeService) || (pool.ValidatorService != "" && !dns.MatchString(pool.ValidatorService)) {
			return fmt.Errorf("invalid Workers runtime pool")
		}
		if err := pool.Execution.validate(); err != nil {
			return fmt.Errorf("Workers runtime pool %s execution: %w", pool.ID, err)
		}
		if pool.RuntimeService == pool.ValidatorService {
			return fmt.Errorf("Workers runtime pool validator service must be distinct from the serving runtime service")
		}
		if seenIDs[pool.ID] || seenServices[pool.RuntimeService] || (pool.ValidatorService != "" && seenServices[pool.ValidatorService]) {
			return fmt.Errorf("duplicate Workers runtime pool id or service")
		}
		seenIDs[pool.ID] = true
		seenServices[pool.RuntimeService] = true
		if pool.ValidatorService != "" {
			seenServices[pool.ValidatorService] = true
		}
		access, err := NormalizeWorkerAccess(pool.Access)
		if err != nil {
			return err
		}
		pool.Access = access
		if err := NormalizeWorkerEgress(pool); err != nil {
			return fmt.Errorf("Workers runtime pool %s: %w", pool.ID, err)
		}
		if err := ValidateWorkerLabels(pool.Labels); err != nil {
			return fmt.Errorf("Workers runtime pool %s labels: %w", pool.ID, err)
		}
		for _, value := range access {
			if !domainAccess[value] {
				return fmt.Errorf("Workers runtime pool access has no configured domain")
			}
		}
		for _, credential := range []struct{ name, token string }{
			{"runtime_token", pool.RuntimeToken},
			{"management_token", pool.ManagementToken},
			{"validator_token", pool.ValidatorToken},
		} {
			if len(credential.token) < 24 || weakCredential(credential.token) {
				return fmt.Errorf("Workers runtime pool %s requires a random %s of at least 24 characters", pool.ID, credential.name)
			}
		}
		if seenRuntimeTokens[pool.RuntimeToken] || seenManagementTokens[pool.ManagementToken] {
			return fmt.Errorf("Workers runtime pools require distinct credentials")
		}
		if pool.ValidatorToken == pool.RuntimeToken || pool.ManagementToken == pool.RuntimeToken || pool.ValidatorToken == pool.ManagementToken {
			return fmt.Errorf("Workers validator and serving credentials must be distinct")
		}
		for _, credential := range []string{pool.RuntimeToken, pool.ValidatorToken, pool.ManagementToken} {
			if seenCredentials[credential] {
				return fmt.Errorf("Workers runtime, validator and management credentials must not be shared across pools")
			}
			seenCredentials[credential] = true
		}
		seenRuntimeTokens[pool.RuntimeToken] = true
		seenManagementTokens[pool.ManagementToken] = true
		if pool.ValidatorManagementBase != "" {
			u, err := url.Parse(pool.ValidatorManagementBase)
			if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
				return fmt.Errorf("invalid Workers runtime pool validator management base")
			}
			pool.ValidatorManagementBase = strings.TrimSuffix(pool.ValidatorManagementBase, "/")
		}
		if pool.Default {
			defaults++
		}
	}
	if c.ValidatorPool != "" {
		pool, ok := c.RuntimePool(c.ValidatorPool)
		if !ok || pool.ValidatorService == "" {
			return fmt.Errorf("Workers validator_pool must select a configured pool with a validator_service")
		}
	}
	if defaults == 0 && len(c.RuntimePools) == 1 {
		c.RuntimePools[0].Default = true
		defaults = 1
	}
	if defaults != 1 {
		return fmt.Errorf("Workers runtime pools require exactly one default")
	}
	defaultsDomains := c.DefaultDomains()
	defaultPool := c.DefaultRuntimePool()
	defaultAccessRaw := []string{}
	for _, id := range defaultsDomains {
		if domain, ok := c.Domain(id); ok {
			defaultAccessRaw = append(defaultAccessRaw, domain.Access)
		}
	}
	defaultAccess, _ := NormalizeWorkerAccess(defaultAccessRaw)
	if !WorkerAccessContains(defaultPool.Access, defaultAccess) {
		return fmt.Errorf("Workers default domains must be supported by the default runtime pool")
	}
	return nil
}

func (c *WorkersConfig) RuntimePool(id string) (WorkerRuntimePool, bool) {
	for _, pool := range c.ConfiguredRuntimePools() {
		if pool.ID == id {
			return pool, true
		}
	}
	return WorkerRuntimePool{}, false
}

func (c *WorkersConfig) DefaultRuntimePool() WorkerRuntimePool {
	for _, pool := range c.ConfiguredRuntimePools() {
		if pool.Default {
			return pool
		}
	}
	if len(c.RuntimePools) > 0 {
		return c.RuntimePools[0]
	}
	return WorkerRuntimePool{}
}

func (c *WorkersConfig) ConfiguredRuntimePools() []WorkerRuntimePool {
	return c.RuntimePools
}

func (pool WorkerRuntimePool) DefaultPeerService() string {
	digest := sha256.Sum256([]byte(pool.ID))
	return "krun-execution-" + hex.EncodeToString(digest[:10])
}

func (pool WorkerRuntimePool) StreamService() string {
	if pool.Execution.Streams == nil {
		return ""
	}
	digest := sha256.Sum256([]byte(pool.ID))
	return "krun-stream-" + hex.EncodeToString(digest[:10])
}

var streamPath = regexp.MustCompile(`^/([A-Za-z0-9_~.-]+/)*[A-Za-z0-9_~.-]+/?$`)

func (e WorkerExecutionPool) validate() error {
	if stream := e.Streams; stream != nil {
		if stream.PerProcess < 0 || stream.PerProcess > 4096 || stream.PerExecutionPod < 0 || stream.PerExecutionPod > 32768 ||
			stream.PerIngressPod < 0 || stream.PerIngressPod > 32768 || stream.PerPeer < 0 || stream.PerPeer > 32768 {
			return fmt.Errorf("invalid stream quota")
		}
		// A wildcard HTTPRoute has 16 rules, including the header rule and short fallback.
		if len(stream.SSEPaths) > 14 {
			return fmt.Errorf("at most fourteen SSE paths fit in an HTTPRoute")
		}
		paths := map[string]bool{}
		for _, path := range stream.SSEPaths {
			if len(path) > 256 || !streamPath.MatchString(path) || paths[path] || strings.Contains(path+"/", "/../") || strings.Contains(path+"/", "/./") {
				return fmt.Errorf("invalid or duplicate SSE path")
			}
			paths[path] = true
		}
	}
	if e.ComputeProcesses != nil && (*e.ComputeProcesses < 0 || *e.ComputeProcesses > 2) {
		return fmt.Errorf("compute_processes must be between 0 and 2")
	}
	for _, quota := range []int{e.OrdinaryInFlight, e.ComputeInFlight, e.PeerOrdinaryInFlight, e.PeerComputeInFlight} {
		if quota < 0 || quota > 4096 {
			return fmt.Errorf("in-flight quota must be between 0 and 4096")
		}
	}
	if e.PeerCooldownMS < 0 || e.PeerCooldownMS > 60000 {
		return fmt.Errorf("peer_cooldown_ms must be between 0 and 60000")
	}
	if e.CPUHighMilli < 0 || e.CPUHighMilli > 1000 || e.CPUReserveMilli < 0 || e.CPUReserveMilli >= 4000 {
		return fmt.Errorf("invalid CPU high watermark or reserve")
	}
	if e.MemoryReserveMiB < 0 || e.MemoryReserveMiB >= 8192 || e.MaxColdInFlight < 0 || e.MaxColdInFlight > 1024 || e.MaxLoadedPerProcess < 0 || e.MaxLoadedPerProcess > 100000 {
		return fmt.Errorf("invalid memory reserve or cold load quota")
	}
	for _, host := range e.ComputeHosts {
		if len(host) > 253 || !originDNS.MatchString(host) {
			return fmt.Errorf("invalid compute host")
		}
	}
	return nil
}
