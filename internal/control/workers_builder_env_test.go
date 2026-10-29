package control

import (
	"strings"
	"testing"
)

func TestLocalBuilderEnvironmentDoesNotInheritControlPlaneSecrets(t *testing.T) {
	t.Setenv("PATH", "/trusted/bin")
	t.Setenv("HTTPS_PROXY", "http://proxy.test:8080")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "must-not-leak")
	t.Setenv("npm_config_userconfig", "/control-plane/.npmrc")
	t.Setenv("NODE_OPTIONS", "--require=/control-plane/inject.js")

	env := localBuilderEnvironment("/build/input.json", "/build/work", "https://registry.test/", "/build/home", "/build/tmp", 600)
	joined := "\n" + strings.Join(env, "\n") + "\n"
	for _, want := range []string{
		"\nPATH=/trusted/bin\n",
		"\nHTTPS_PROXY=http://proxy.test:8080\n",
		"\nHOME=/build/home\n",
		"\nTMPDIR=/build/tmp\n",
		"\nKRUN_BUILD_PHASE=all\n",
		"\nKRUN_BUILD_INPUT=/build/input.json\n",
		"\nKRUN_BUILD_WORK=/build/work\n",
		"\nKRUN_NPM_REGISTRY=https://registry.test/\n",
		"\nKRUN_BUILD_TIMEOUT_MS=570000\n",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("builder environment is missing %q: %v", strings.TrimSpace(want), env)
		}
	}
	for _, forbidden := range []string{"AWS_SECRET_ACCESS_KEY", "npm_config_userconfig", "NODE_OPTIONS", "must-not-leak", ".npmrc", "inject.js"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("builder environment leaked %q: %v", forbidden, env)
		}
	}
}
