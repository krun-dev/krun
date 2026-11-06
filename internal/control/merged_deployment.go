package control

import (
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"

	"github.com/krun-dev/krun/internal/domain"
)

// The compatibility listener goes only to this process's configured internal
// address. Runtime handlers retain their pool authentication and path handling.
func (s *Server) mergedRuntimeProxy() http.Handler {
	host, port, _ := net.SplitHostPort(s.Config.InternalListen)
	switch host {
	case "", "0.0.0.0":
		host = "127.0.0.1"
	case "::":
		host = "::1"
	}
	target := &url.URL{Scheme: "http", Host: net.JoinHostPort(host, port)}
	proxy := httputil.NewSingleHostReverseProxy(target)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// A local Runtime API must never be sent through a process HTTP proxy.
	transport.Proxy = nil
	proxy.Transport = transport
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, _ error) {
		s.writeError(w, r, domain.Problem(http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", "Runtime API temporarily unavailable"))
	}
	return proxy
}
