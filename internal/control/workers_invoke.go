package control

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	workerInvokePathLimit      = 2048
	workerInvokeBodyLimit      = 64 << 10
	workerInvokeResponseLimit  = 64 << 10
	workerInvokeTimeout        = 10 * time.Second
	workerInvokeInvalidMessage = "Use a relative path and a supported HTTP method; body at most 64 KiB"
)

type workerInvokeRequest struct {
	Path   string `json:"path"`
	Method string `json:"method"`
	Body   string `json:"body"`
}

type workerInvokeResponse struct {
	Status    int               `json:"status"`
	Headers   map[string]string `json:"headers"`
	Body      string            `json:"body"`
	Truncated bool              `json:"truncated"`
}

func newWorkerInvokeClient() *http.Client {
	return &http.Client{
		Timeout: workerInvokeTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// Console invocation returns business responses as JSON data. It never serves
// business HTML, cookies, or redirects on the console origin.
func (s *Server) workerInvoke(w http.ResponseWriter, r *http.Request) {
	var in workerInvokeRequest
	if err := body(r, &in); err != nil {
		s.writeError(w, r, err)
		return
	}
	path, err := in.validate()
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	host, err := s.workerInvokeHost(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out, err := s.invokeWorker(r.Context(), host, in, path)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.json(w, http.StatusOK, out)
}

func (in *workerInvokeRequest) validate() (*url.URL, error) {
	if in.Path == "" {
		in.Path = "/"
	}
	if in.Method == "" {
		in.Method = http.MethodGet
	}
	if len(in.Path) > workerInvokePathLimit || len(in.Body) > workerInvokeBodyLimit {
		return nil, problem(422, "WORKER_INVOKE_INVALID", workerInvokeInvalidMessage)
	}
	switch in.Method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete, http.MethodOptions:
	default:
		return nil, problem(422, "WORKER_INVOKE_INVALID", workerInvokeInvalidMessage)
	}
	path, err := url.ParseRequestURI(in.Path)
	if err != nil {
		return nil, problem(422, "WORKER_INVOKE_INVALID", workerInvokeInvalidMessage)
	}
	if path.IsAbs() || path.Host != "" || !strings.HasPrefix(in.Path, "/") || strings.HasPrefix(in.Path, "//") {
		return nil, problem(422, "WORKER_INVOKE_INVALID", workerInvokeInvalidMessage)
	}
	return path, nil
}

func (s *Server) workerInvokeHost(r *http.Request) (string, error) {
	var host string
	var appErr error
	err := s.Store.Update(r.Context(), func(st *State) error {
		s.cleanup(st)
		principal, err := s.protectedPrincipal(st, r, false)
		if err != nil {
			appErr = err
			return nil
		}
		env := r.PathValue("environment")
		if !workerEnvValid(env) {
			appErr = notFound()
			return nil
		}
		target, err := s.workerTarget(st, r, env, "service.invoke", principal)
		if err != nil {
			// Commit the denied authorization audit rather than rolling it back.
			appErr = err
			return nil
		}
		service := st.Workers.Services[target.ID]
		current := service.Environments[env]
		if !current.Enabled || current.Deployment == "" {
			appErr = problem(409, "WORKER_NOT_PUBLISHED", "Publish this enabled environment first")
			return nil
		}
		host = current.Host
		return nil
	})
	if err != nil {
		return "", err
	}
	return host, appErr
}

// Keep network I/O outside Store.Update: a slow Worker must not hold the IAM lock.
func (s *Server) invokeWorker(ctx context.Context, host string, in workerInvokeRequest, path *url.URL) (workerInvokeResponse, error) {
	var out workerInvokeResponse
	target := *path
	target.Scheme = "http"
	target.Host = s.Config.Workers.GatewayName + "." + s.Config.Workers.GatewayNamespace + ".svc:8080"
	req, err := http.NewRequestWithContext(ctx, in.Method, target.String(), strings.NewReader(in.Body))
	if err != nil {
		return out, err
	}
	req.Host = host
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.workerInvokeClient.Do(req)
	if err != nil {
		return out, problem(503, "WORKER_INVOKE_UNAVAILABLE", "Worker invocation unavailable")
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, workerInvokeResponseLimit+1))
	if err != nil {
		return out, problem(503, "WORKER_INVOKE_UNAVAILABLE", "Worker response interrupted")
	}
	out.Status = resp.StatusCode
	out.Truncated = len(raw) > workerInvokeResponseLimit
	if out.Truncated {
		raw = raw[:workerInvokeResponseLimit]
	}
	out.Body = string(raw)
	out.Headers = make(map[string]string)
	for _, name := range []string{"Content-Type", "X-Krun-Deployment", "X-Krun-Pod"} {
		if value := resp.Header.Get(name); value != "" {
			out.Headers[name] = value
		}
	}
	return out, nil
}
