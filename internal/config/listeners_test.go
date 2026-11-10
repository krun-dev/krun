package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestLoadConfigDevelopmentListenersRequireLoopback(t *testing.T) {
	template, err := os.ReadFile(filepath.Join("..", "..", "config", "development.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	for _, field := range []string{"listen", "internal_listen"} {
		for _, tc := range []struct {
			name    string
			host    string
			allowed bool
		}{
			{"IPv4 loopback", "127.0.0.2", true},
			{"IPv6 loopback", "[::1]", true},
			{"IPv4 wildcard", "0.0.0.0", false},
			{"IPv6 wildcard", "[::]", false},
			{"empty host", "", false},
			{"non-loopback IP", "192.0.2.1", false},
			{"hostname", "localhost", false},
		} {
			t.Run(field+"/"+tc.name, func(t *testing.T) {
				line := field + ": " + strconv.Quote(tc.host+":9000") + "\n"
				yaml := string(template)
				if field == "listen" {
					yaml = strings.Replace(yaml, "listen: 127.0.0.1:8080\n", line, 1)
				} else {
					yaml += "\n" + line
				}

				path := filepath.Join(t.TempDir(), "server.yaml")
				if err := os.WriteFile(path, []byte(yaml), 0600); err != nil {
					t.Fatal(err)
				}
				_, err := LoadConfig(path)
				if tc.allowed {
					if err != nil {
						t.Fatal(err)
					}
					return
				}
				if err == nil || !strings.Contains(err.Error(), "loopback "+field) {
					t.Fatalf("expected a loopback error for %s, got %v", field, err)
				}
			})
		}
	}
}
