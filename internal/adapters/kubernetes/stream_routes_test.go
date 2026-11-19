package kubernetes

import (
	"regexp"
	"testing"

	"github.com/krun-dev/krun/internal/config"
)

func TestManagedStreamRulesPreserveOriginsAndExactPath(t *testing.T) {
	rules := []HostnameRule{
		{Match: "prefix", Path: "/", Service: "runtime", Port: 8081, Worker: "site", StreamService: "stream",
			SSEPaths: []string{"/model", "/assets/file", "/exact"}},
		{Match: "prefix", Path: "/assets", Service: "origin", Port: 8081},
		{Match: "exact", Path: "/exact", Service: "runtime", Port: 8081, Worker: "exact-site", StreamService: "stream", SSEPaths: []string{"/exact"}},
	}
	base, supplemental := hostnameRouteSpecs("host.example.test", config.WorkerGateway{}, rules)
	entries := base["rules"].([]any)
	if len(entries) != 3 || len(supplemental) != 2 {
		t.Fatalf("want three base rules and two streaming rules, got %v and %v", entries, supplemental)
	}
	exact := entries[2].(map[string]any)
	exactBackend := exact["backendRefs"].([]any)[0].(map[string]any)["name"]
	if exactBackend != "stream" {
		t.Fatalf("configured exact path should move the base rule to streams: %v", exact)
	}
	headerRule := supplemental[0].(map[string]any)
	options := headerRule["matches"].([]any)
	if len(options) != 2 {
		t.Fatalf("want Upgrade and Accept alternatives: %v", options)
	}
	accept := options[1].(map[string]any)["headers"].([]any)[0].(map[string]string)
	matcher := regexp.MustCompile(accept["value"])
	if !matcher.MatchString("application/json, text/event-stream; q=1") || matcher.MatchString("application/json") {
		t.Fatalf("invalid SSE Accept matcher %q", matcher)
	}
	model := supplemental[1].(map[string]any)["matches"].([]any)[0].(map[string]any)["path"].(map[string]string)
	if model["value"] != "/model" {
		t.Fatalf("origin won the more specific path or wrong Worker path matched: %v", model)
	}
}
