package repositoryoauth

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	identityprovider "github.com/krun-dev/krun/internal/adapters/identity"
	appconfig "github.com/krun-dev/krun/internal/config"
	"golang.org/x/oauth2"
)

type Client struct{ Config *appconfig.Config }

func (s *Client) RepositoryOAuthConfig(p appconfig.WorkerRepositoryProvider) *oauth2.Config {
	if p.ClientID == "" {
		return nil
	}
	endpoint := oauth2.Endpoint{AuthURL: p.BaseURL + "/oauth/authorize", TokenURL: p.BaseURL + "/oauth/token"}
	if p.Type == "github" {
		endpoint = oauth2.Endpoint{AuthURL: "https://github.com/login/oauth/authorize", TokenURL: "https://github.com/login/oauth/access_token"}
	}
	scopes := []string{"read_repository"}
	if p.Type == "github" {
		scopes = []string{"repo"}
	}
	return &oauth2.Config{ClientID: p.ClientID, ClientSecret: p.ClientSecret, Endpoint: endpoint, Scopes: scopes, RedirectURL: s.Config.PublicURL + "/api/v1/workers/repository/" + p.ID + "/callback"}
}

// repositoryOAuthContext confines client credentials to the configured token
// endpoint. Redirects are rejected before net/http can replay a POST carrying
// client_secret, and the transport cannot dial a different host or port.
func (s *Client) RepositoryOAuthContext(parent context.Context, provider appconfig.WorkerRepositoryProvider) (context.Context, context.CancelFunc, error) {
	confBase := strings.TrimSuffix(provider.BaseURL, "/")
	tokenURL := confBase + "/oauth/token"
	if provider.Type == "github" {
		tokenURL = "https://github.com/login/oauth/access_token"
	}
	endpoint, err := url.Parse(tokenURL)
	if err != nil || endpoint.Hostname() == "" {
		return nil, nil, errors.New("invalid repository OAuth token endpoint")
	}
	expectedHost := strings.ToLower(endpoint.Hostname())
	expectedIP := net.ParseIP(expectedHost)
	loopbackHost := expectedHost == "localhost" || expectedIP != nil && expectedIP.IsLoopback()
	if endpoint.Scheme != "https" && !(s.Config.Development && endpoint.Scheme == "http" && loopbackHost) {
		return nil, nil, errors.New("repository OAuth token endpoint requires HTTPS")
	}
	expectedPort := endpoint.Port()
	if expectedPort == "" {
		if endpoint.Scheme == "https" {
			expectedPort = "443"
		} else {
			expectedPort = "80"
		}
	}
	transport := &http.Transport{
		Proxy:                  nil,
		TLSHandshakeTimeout:    5 * time.Second,
		ResponseHeaderTimeout:  5 * time.Second,
		MaxResponseHeaderBytes: 32 << 10,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, splitErr := net.SplitHostPort(address)
			if splitErr != nil || strings.ToLower(host) != expectedHost || port != expectedPort {
				return nil, errors.New("repository OAuth upstream host not allowed")
			}
			ips, resolveErr := net.DefaultResolver.LookupIPAddr(ctx, host)
			if resolveErr != nil {
				return nil, resolveErr
			}
			if len(ips) == 0 {
				return nil, errors.New("repository OAuth upstream has no addresses")
			}
			for _, candidate := range ips {
				private := candidate.IP.IsLoopback() || candidate.IP.IsPrivate()
				alwaysUnsafe := candidate.IP.IsUnspecified() || candidate.IP.IsLinkLocalUnicast() || candidate.IP.IsLinkLocalMulticast()
				loopbackTest := s.Config.Development && candidate.IP.IsLoopback() && loopbackHost
				if alwaysUnsafe || private && !loopbackTest && !provider.AllowPrivateNetwork {
					return nil, errors.New("unsafe repository OAuth upstream address")
				}
			}
			dialer := net.Dialer{Timeout: 5 * time.Second}
			return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
		},
	}
	client := &http.Client{
		Transport: RepositoryOAuthResponseLimit{base: transport},
		Timeout:   15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return identityprovider.ErrUpstreamRedirect
		},
	}
	ctx, stop := context.WithTimeout(parent, 15*time.Second)
	cancel := func() {
		stop()
		transport.CloseIdleConnections()
	}
	return context.WithValue(ctx, oauth2.HTTPClient, client), cancel, nil
}

const RepositoryOAuthResponseLimitBytes = 64 << 10

type RepositoryOAuthResponseLimit struct {
	base http.RoundTripper
}

func (t RepositoryOAuthResponseLimit) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(request)
	if err == nil && response.Body != nil {
		response.Body = &RepositoryOAuthBody{body: response.Body, remaining: RepositoryOAuthResponseLimitBytes}
	}
	return response, err
}

type RepositoryOAuthBody struct {
	body      io.ReadCloser
	remaining int64
}

func (b *RepositoryOAuthBody) Read(buffer []byte) (int, error) {
	if b.remaining == 0 {
		var probe [1]byte
		n, err := b.body.Read(probe[:])
		if n > 0 {
			return 0, errors.New("repository OAuth response exceeds limit")
		}
		return 0, err
	}
	if int64(len(buffer)) > b.remaining {
		buffer = buffer[:b.remaining]
	}
	n, err := b.body.Read(buffer)
	b.remaining -= int64(n)
	return n, err
}

func (b *RepositoryOAuthBody) Close() error { return b.body.Close() }
