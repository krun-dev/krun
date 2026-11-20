package control

import (
	"net"
	"net/http"
	"net/url"
	"strings"
)

func (s *Server) consoleDomainRedirect(source *url.URL) func(http.ResponseWriter, *http.Request) bool {
	redirect := s.Config.DomainRedirect
	if redirect == nil {
		return nil
	}
	sourceHost := strings.TrimSuffix(source.Hostname(), ".")
	target := strings.TrimSuffix(redirect.TargetURL, "/")
	ips := make(map[string]struct{}, len(redirect.IPs))
	for _, value := range redirect.IPs {
		ips[net.ParseIP(value).String()] = struct{}{}
	}

	return func(w http.ResponseWriter, r *http.Request) bool {
		authority := url.URL{Host: r.Host}
		if !strings.EqualFold(strings.TrimSuffix(authority.Hostname(), "."), sourceHost) {
			return false
		}

		// The result depends on the client's network, including when serving the file.
		w.Header().Set("Cache-Control", "no-store")
		ip := s.consoleRedirectIP(r)
		if _, ok := ips[ip.String()]; !ok {
			return false
		}
		http.Redirect(w, r, target+r.URL.RequestURI(), http.StatusFound)
		return true
	}
}

func (s *Server) consoleRedirectIP(r *http.Request) net.IP {
	if !s.trustedProxy(net.ParseIP(requestPeerHost(r))) {
		return nil
	}
	for _, name := range []string{"X-Forwarded-For", "X-Forward-For"} {
		if values := r.Header.Values(name); len(values) > 0 {
			return s.forwardedClientIP(values)
		}
	}
	if values := r.Header.Values("X-Real-IP"); len(values) == 1 {
		return net.ParseIP(strings.TrimSpace(values[0]))
	}
	return nil
}
