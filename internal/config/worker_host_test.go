package config

import (
	"strings"
	"testing"
)

func TestWorkerHostnamesUseNameAndPreviewPrefix(t *testing.T) {
	for _, tc := range []struct{ name, environment, want string }{
		{"orders", "production", "orders.workers.test"},
		{"orders", "feature", "feature-orders.workers.test"},
		{"orders", "pr-123", "pr-123-orders.workers.test"},
	} {
		if got := HostFor(tc.name, tc.environment, "workers.test"); got != tc.want {
			t.Errorf("HostFor(%q,%q) = %q, want %q", tc.name, tc.environment, got, tc.want)
		}
	}
	if got := HostFor(strings.Repeat("a", 63), "preview", "workers.test"); got != "" {
		t.Fatalf("overlong preview label was accepted: %q", got)
	}
}

func TestWorkerPreviewNamesAreDNSLabels(t *testing.T) {
	for _, invalid := range []string{"UPPER", "has_underscore", "-start", "production", "global", "development", strings.Repeat("a", 64)} {
		if WorkerPreviewValid(invalid) {
			t.Errorf("invalid preview name accepted: %q", invalid)
		}
	}
	if !WorkerPreviewValid("pr-123") {
		t.Fatal("valid preview name rejected")
	}
}
