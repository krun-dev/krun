package invoke

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	appconfig "github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/domain"
)

type Doer interface {
	Do(*http.Request) (*http.Response, error)
}
type Client struct {
	Config *appconfig.WorkersConfig
	HTTP   Doer
}

const (
	InvokePathLimit      = 2048
	InvokeBodyLimit      = 64 << 10
	InvokeResponseLimit  = 64 << 10
	InvokeTimeout        = 10 * time.Second
	InvokeInvalidMessage = "Use a relative path and a supported HTTP method; body at most 64 KiB"
)

type Request struct {
	Path   string `json:"path"`
	Method string `json:"method"`
	Body   string `json:"body"`
}

type Response struct {
	Status    int               `json:"status"`
	Headers   map[string]string `json:"headers"`
	Body      string            `json:"body"`
	Truncated bool              `json:"truncated"`
}

func NewHTTPClient() *http.Client {
	return &http.Client{
		Timeout: InvokeTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func (in *Request) Validate() (*url.URL, error) {
	if in.Path == "" {
		in.Path = "/"
	}
	if in.Method == "" {
		in.Method = http.MethodGet
	}
	if len(in.Path) > InvokePathLimit || len(in.Body) > InvokeBodyLimit {
		return nil, domain.Problem(422, "WORKER_INVOKE_INVALID", InvokeInvalidMessage)
	}
	switch in.Method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete, http.MethodOptions:
	default:
		return nil, domain.Problem(422, "WORKER_INVOKE_INVALID", InvokeInvalidMessage)
	}
	path, err := url.ParseRequestURI(in.Path)
	if err != nil {
		return nil, domain.Problem(422, "WORKER_INVOKE_INVALID", InvokeInvalidMessage)
	}
	if path.IsAbs() || path.Host != "" || !strings.HasPrefix(in.Path, "/") || strings.HasPrefix(in.Path, "//") {
		return nil, domain.Problem(422, "WORKER_INVOKE_INVALID", InvokeInvalidMessage)
	}
	return path, nil
}
func (s *Client) Call(ctx context.Context, host, gatewayID string, in Request, path *url.URL) (Response, error) {
	var out Response
	g, ok := s.Config.Gateway(gatewayID)
	if !ok {
		return out, domain.Problem(503, "WORKER_INVOKE_UNAVAILABLE", "Worker gateway not configured")
	}
	target := *path
	if base := g.GatewayBase(); base != "" {
		parsed, e := url.Parse(base)
		if e != nil {
			return out, domain.Problem(503, "WORKER_INVOKE_UNAVAILABLE", "Worker gateway not configured")
		}
		target.Scheme = parsed.Scheme
		target.Host = parsed.Host
	} else {
		target.Scheme = "http"
		target.Host = g.InClusterAddress()
	}
	req, err := http.NewRequestWithContext(ctx, in.Method, target.String(), strings.NewReader(in.Body))
	if err != nil {
		return out, err
	}
	req.Host = host
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.HTTP.Do(req)
	if err != nil {
		return out, domain.Problem(503, "WORKER_INVOKE_UNAVAILABLE", "Worker invocation unavailable")
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, InvokeResponseLimit+1))
	if err != nil {
		return out, domain.Problem(503, "WORKER_INVOKE_UNAVAILABLE", "Worker response interrupted")
	}
	out.Status = resp.StatusCode
	out.Truncated = len(raw) > InvokeResponseLimit
	if out.Truncated {
		raw = raw[:InvokeResponseLimit]
	}
	out.Body = string(raw)
	out.Headers = make(map[string]string)
	for _, name := range []string{"Content-Type", "X-Krun-Version", "X-Krun-Pod"} {
		if value := resp.Header.Get(name); value != "" {
			out.Headers[name] = value
		}
	}
	return out, nil
}
