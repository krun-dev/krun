package runtimeclient

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"

	appconfig "github.com/krun-dev/krun/internal/config"
)

func CallService(ctx context.Context, client Doer, pool appconfig.WorkerRuntimePool, podIP, host, version, requestURL, method string, headers http.Header, body io.Reader) (*http.Response, error) {
	target := "http://" + net.JoinHostPort(podIP, "8083") + "/service-invoke"
	request, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, err
	}
	request.Header = headers.Clone()
	StripHopHeaders(request.Header)
	appAuth := request.Header.Get("X-Krun-Service-App-Authorization")
	for name := range request.Header {
		if strings.HasPrefix(strings.ToLower(name), "x-krun-service-") {
			request.Header.Del(name)
		}
	}
	request.Header.Del("X-Runtime-Pool")
	request.Header.Set("Authorization", "Bearer "+pool.ManagementToken)
	request.Header.Set("X-Krun-Service-Host", host)
	request.Header.Set("X-Krun-Service-URL", requestURL)
	if appAuth != "" {
		request.Header.Set("X-Krun-Service-App-Authorization", appAuth)
	}
	if version != "" {
		request.Header.Set("X-Krun-Service-Version", version)
	}
	// This private hop must not follow redirects returned by application code.
	if httpClient, ok := client.(*http.Client); ok {
		clone := *httpClient
		clone.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		return clone.Do(request)
	}
	return client.Do(request)
}

func StripHopHeaders(headers http.Header) {
	for _, value := range headers.Values("Connection") {
		for _, name := range strings.Split(value, ",") {
			headers.Del(strings.TrimSpace(name))
		}
	}
	for _, name := range []string{"Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization",
		"TE", "Trailer", "Transfer-Encoding", "Upgrade", "Proxy-Connection", "Content-Length"} {
		headers.Del(name)
	}
}
