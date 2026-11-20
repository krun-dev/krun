package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDomainRedirectLoadsThroughMainConfig(t *testing.T) {
	const main = `
public_url: https://console.example.com
db: postgres://fixture/krun_test
static_dir: dist/console
trusted_proxy_cidrs: [192.0.2.0/24]
`
	const redirect = `
domain_redirect:
  ips: [198.51.100.10, "2001:db8::10"]
  target_url: ${KRUN_CONSOLE_REDIRECT_TARGET}
`
	t.Setenv("KRUN_CONSOLE_REDIRECT_TARGET", "https://console-internal.example.com/")
	for _, tc := range []struct {
		name string
		yaml string
		want string
	}{
		{name: "disabled", yaml: main},
		{name: "enabled", yaml: main + redirect},
		{name: "no_static_dir", yaml: strings.Replace(main, "static_dir: dist/console", "", 1) + redirect, want: "requires static_dir"},
		{name: "no_trusted_proxy", yaml: strings.Replace(main, "192.0.2.0/24", "", 1) + redirect, want: "requires trusted_proxy_cidrs"},
		{name: "no_addresses", yaml: main + strings.Replace(redirect, `198.51.100.10, "2001:db8::10"`, "", 1), want: "requires at least one IP"},
		{name: "invalid_address", yaml: main + strings.Replace(redirect, "198.51.100.10", "198.51.100.0/24", 1), want: "invalid domain_redirect.ips"},
		{name: "same_host", yaml: main + strings.Replace(redirect, "${KRUN_CONSOLE_REDIRECT_TARGET}", "https://CONSOLE.example.com.:443", 1), want: "different hostname"},
		{name: "target_subpath", yaml: main + strings.Replace(redirect, "${KRUN_CONSOLE_REDIRECT_TARGET}", "https://internal.example.com/console", 1), want: "must be an origin"},
		{name: "insecure_target", yaml: main + strings.Replace(redirect, "${KRUN_CONSOLE_REDIRECT_TARGET}", "http://internal.example.com", 1), want: "requires HTTPS"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "main.yaml")
			if err := os.WriteFile(path, []byte(tc.yaml), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadConfig(path)
			if tc.want != "" {
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("LoadConfig error = %v, want %q", err, tc.want)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.name == "enabled" {
				if cfg.DomainRedirect == nil || len(cfg.DomainRedirect.IPs) != 2 ||
					cfg.DomainRedirect.TargetURL != "https://console-internal.example.com/" {
					t.Fatalf("unexpected domain redirect config: %+v", cfg.DomainRedirect)
				}
			} else if cfg.DomainRedirect != nil {
				t.Fatal("domain_redirect should be opt-in")
			}
		})
	}
}
