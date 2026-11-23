package config

import "testing"

func TestRuntimeResourceDefaultsAndBudgets(t *testing.T) {
	limits := WorkerRuntimeConfig{}
	if err := limits.Validate(); err != nil {
		t.Fatal(err)
	}
	if limits.Protocol != 2 || limits.VerifyMillis != 5000 || limits.CacheEntries != 65536 || limits.PodColdConcurrent != 4 {
		t.Fatalf("unexpected defaults: %+v", limits)
	}
	for name, change := range map[string]func(*WorkerRuntimeConfig){
		"unknown protocol":    func(c *WorkerRuntimeConfig) { c.Protocol = 3 },
		"unbounded reads":     func(c *WorkerRuntimeConfig) { c.ReadConcurrency = 257 },
		"tiny frame":          func(c *WorkerRuntimeConfig) { c.FrameBytes = 512 },
		"oversize resource":   func(c *WorkerRuntimeConfig) { c.ResourceBytes = 16 << 20; c.PodCacheBytes = 1 << 20 },
		"unbounded freshness": func(c *WorkerRuntimeConfig) { c.VerifyMillis = 60001 },
	} {
		t.Run(name, func(t *testing.T) {
			copy := limits
			change(&copy)
			if err := copy.Validate(); err == nil {
				t.Fatal("invalid budget accepted")
			}
		})
	}
	legacy := WorkerRuntimeConfig{Protocol: 1}
	if err := legacy.Validate(); err != nil {
		t.Fatal(err)
	}
}
