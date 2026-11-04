package control

import (
	"net"
	"net/url"
	"strings"
)

func (s *Server) consoleContentSecurityPolicy() string {
	sources := []string{"'self'"}
	seen := map[string]bool{}
	if s.Config.Workers != nil && s.Config.Workers.ObjectStorage != nil {
		for _, store := range s.Config.Workers.ObjectStorage.Stores {
			u, err := url.Parse(store.Endpoint)
			if err != nil {
				continue
			}
			if store.AddressingStyle == "virtual" {
				host := store.Bucket + "." + u.Hostname()
				if u.Port() != "" {
					host = net.JoinHostPort(host, u.Port())
				}
				u.Host = host
			}
			origin := u.Scheme + "://" + u.Host
			if !strings.ContainsAny(origin, "'; \t\r\n") && !seen[origin] {
				seen[origin] = true
				sources = append(sources, origin)
			}
		}
	}
	return "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src " + strings.Join(sources, " ") + "; frame-ancestors 'none'; base-uri 'self'; form-action 'self'"
}
