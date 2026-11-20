package control

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	appconfig "github.com/krun-dev/krun/internal/config"
)

func TestConsoleDomainRedirect(t *testing.T) {
	const source = "https://console.example.com"
	const destination = "https://console-internal.example.com"
	dir := t.TempDir()
	for _, path := range []string{"index.html", "install.sh", "downloads/manifest.json", "downloads/krun-linux-amd64", "assets/app.js", "brand/logo.png"} {
		filename := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filename, []byte("console"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	_, proxy, err := net.ParseCIDR("192.0.2.0/24")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{
		Config: appconfig.Config{
			PublicURL: source,
			StaticDir: dir,
			DomainRedirect: &appconfig.DomainRedirect{
				IPs:       []string{"198.51.100.10", "198.51.100.11", "2001:0db8::10"},
				TargetURL: destination + "/",
			},
		},
		trustedProxies: []*net.IPNet{proxy},
	}
	s.routes()

	for _, uri := range []string{
		"/?oauth=request-id",
		"/?oauth_device=1",
		"/install.sh",
		"/downloads/manifest.json",
		"/downloads/krun-linux-amd64",
		"/assets/app.js",
		"/brand/logo.png",
	} {
		t.Run(uri, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, source+uri, nil)
			req.RemoteAddr = "192.0.2.4:12345"
			req.Header.Set("X-Forwarded-For", "198.51.100.10")
			res := httptest.NewRecorder()
			s.ServeHTTP(res, req)
			if res.Code != http.StatusOK || res.Body.String() != "console" {
				t.Fatalf("got status %d Location %q, want original file", res.Code, res.Header().Get("Location"))
			}
		})
	}

	for _, tc := range []struct {
		name     string
		origin   string
		uri      string
		method   string
		remote   string
		headers  http.Header
		status   int
		location string
	}{
		{
			name: "trusted_chain_preserves_request_uri", uri: "/docs/a%2Fb?q=a%2Bb&next=%2F",
			headers: http.Header{"X-Forwarded-For": {"198.51.100.10", "192.0.2.5"}},
			status:  http.StatusFound, location: destination + "/docs/a%2Fb?q=a%2Bb&next=%2F",
		},
		{
			name: "legacy_forward_for", headers: http.Header{"X-Forward-For": {"198.51.100.11"}},
			status: http.StatusFound, location: destination + "/",
		},
		{
			name: "head_and_real_ipv6", method: http.MethodHead, uri: "/?",
			headers: http.Header{"X-Real-Ip": {"2001:db8::10"}},
			status:  http.StatusFound, location: destination + "/?",
		},
		{
			name: "untrusted_peer", remote: "203.0.113.6:12345",
			headers: http.Header{"X-Forwarded-For": {"198.51.100.10"}, "X-Real-Ip": {"198.51.100.10"}},
			status:  http.StatusOK,
		},
		{
			name: "spoofed_forwarded_prefix_and_real_ip",
			headers: http.Header{
				"X-Forwarded-For": {"198.51.100.10, 203.0.113.6, 192.0.2.5"},
				"X-Real-Ip":       {"198.51.100.10"},
			},
			status: http.StatusOK,
		},
		{
			name: "malformed_forwarded_chain",
			headers: http.Header{
				"X-Forwarded-For": {"198.51.100.10, invalid"},
				"X-Real-Ip":       {"198.51.100.10"},
			},
			status: http.StatusOK,
		},
		{
			name:    "conflicting_real_ip",
			headers: http.Header{"X-Real-Ip": {"198.51.100.10", "203.0.113.6"}},
			status:  http.StatusOK,
		},
		{
			name: "destination_host", origin: destination,
			headers: http.Header{"X-Real-Ip": {"198.51.100.10"}},
			status:  http.StatusOK,
		},
		{
			name: "api", uri: "/api/auth/providers",
			headers: http.Header{"X-Real-Ip": {"198.51.100.10"}},
			status:  http.StatusOK,
		},
		{
			name: "post", method: http.MethodPost,
			headers: http.Header{"X-Real-Ip": {"198.51.100.10"}},
			status:  http.StatusNotFound,
		},
		{
			name: "missing_headers", status: http.StatusOK,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			origin, uri, method, remote := tc.origin, tc.uri, tc.method, tc.remote
			if origin == "" {
				origin = source
			}
			if uri == "" {
				uri = "/"
			}
			if method == "" {
				method = http.MethodGet
			}
			if remote == "" {
				remote = "192.0.2.4:12345"
			}
			req := httptest.NewRequest(method, origin+uri, nil)
			req.RemoteAddr = remote
			for name, values := range tc.headers {
				for _, value := range values {
					req.Header.Add(name, value)
				}
			}
			res := httptest.NewRecorder()
			s.ServeHTTP(res, req)
			if location := res.Header().Get("Location"); res.Code != tc.status || location != tc.location {
				t.Fatalf("got status %d Location %q, want %d %q", res.Code, location, tc.status, tc.location)
			}
			if tc.status == http.StatusFound && res.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("IP-specific redirect must not be cached")
			}
		})
	}
}
