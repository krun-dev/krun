package config

import "fmt"

// WorkerRuntimeConfig bounds configuration memory, authority checks and cold
// reads across each control-plane replica. Replicas share the deployment budget.
type WorkerRuntimeConfig struct {
	Protocol          int   `json:"protocol"`
	VerifyMillis      int   `json:"verify_millis"`
	CacheEntries      int   `json:"cache_entries"`
	CacheBytes        int64 `json:"cache_bytes"`
	ReadConcurrency   int   `json:"read_concurrency"`
	MaxSubscriptions  int   `json:"max_subscriptions"`
	MaxConnections    int   `json:"max_connections"`
	FrameBytes        int   `json:"frame_bytes"`
	ResourceBytes     int   `json:"resource_bytes"`
	PodCacheBytes     int64 `json:"pod_cache_bytes"`
	PodCacheEntries   int   `json:"pod_cache_entries"`
	PodColdConcurrent int   `json:"pod_cold_concurrency"`
}

func (c *WorkerRuntimeConfig) Validate() error {
	if c.Protocol == 0 {
		c.Protocol = 2
	}
	defaults := []struct {
		value                      *int
		fallback, minimum, maximum int
	}{
		{&c.VerifyMillis, 5000, 100, 60000},
		{&c.CacheEntries, 65536, 1, 1000000},
		{&c.ReadConcurrency, 32, 1, 256},
		{&c.MaxSubscriptions, 65536, 1, 100000},
		{&c.MaxConnections, 1024, 1, 100000},
		{&c.FrameBytes, 262144, 4096, 1048576},
		{&c.ResourceBytes, 4194304, 4096, 16777216},
		{&c.PodCacheEntries, 65536, 16, 100000},
		{&c.PodColdConcurrent, 4, 1, 64},
	}
	for _, d := range defaults {
		if *d.value == 0 {
			*d.value = d.fallback
		}
		if *d.value < d.minimum || *d.value > d.maximum {
			return fmt.Errorf("workers.runtime_config value outside supported bounds")
		}
	}
	if c.CacheBytes == 0 {
		c.CacheBytes = 128 << 20
	}
	if c.PodCacheBytes == 0 {
		c.PodCacheBytes = 64 << 20
	}
	if c.Protocol != 1 && c.Protocol != 2 || c.CacheBytes < 1<<20 || c.CacheBytes > 2<<30 || c.PodCacheBytes < 1<<20 || c.PodCacheBytes > 2<<30 || c.ResourceBytes > int(c.CacheBytes*3/4) || c.ResourceBytes > int(c.PodCacheBytes*3/4) {
		return fmt.Errorf("invalid workers.runtime_config limits or protocol")
	}
	return nil
}
