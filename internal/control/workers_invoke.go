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
	if !s.requireWorkers(w, r) {
		return
	}
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
	host, gatewayID, err := s.workerInvokeTarget(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out, err := s.invokeWorker(r.Context(), host, gatewayID, in, path)
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

// workerInvokeTarget resolves the upstream host for an invoke call. It reads
// both partitions because authorization needs IAM while routing needs Workers,
// and it writes nothing: a denial is audited separately.
func (s *Server) workerInvokeTarget(r *http.Request) (string, string, error) {
	c, e := s.readCredentials(r)
	if e != nil {
		return "", "", e
	}
	var host, gatewayID string
	var appErr error
	var principal Principal
	err := s.Store.View(r.Context(), PartitionAll, func(st *State) error {
		principal, e = s.protectedPrincipal(st, c, r, false)
		if e != nil {
			appErr = e
			return nil
		}
		env := r.PathValue("environment")
		if !workerEnvValid(env) {
			appErr = notFound()
			return nil
		}
		target, err := s.workerTarget(st, r, env, "service.invoke", principal)
		if err != nil {
			appErr = err
			return nil
		}
		service := st.Workers.Services[target.ID]
		current := service.Environments[env]
		if !current.Enabled || current.Release == "" {
			appErr = problem(409, "WORKER_NOT_PUBLISHED", "Publish this enabled environment first")
			return nil
		}
		// Invoke through the first selected domain of the service. The domain's
		// Gateway decides which instance to address. Hosts are derived, not
		// stored, so they come from the catalog and the service selection.
		slug := workerSlug(service.Organization, service.Service)
		for _, id := range s.syncServiceDomains(service) {
			d, ok := s.Config.Workers.domain(id)
			if !ok {
				continue
			}
			if candidate := hostFor(slug, env, d.Domain); candidate != "" {
				host, gatewayID = candidate, d.Gateway
				return nil
			}
		}
		appErr = problem(409, "WORKER_NOT_PUBLISHED", "Environment has no routable domain")
		return nil
	})
	if err != nil {
		return "", "", err
	}
	if appErr != nil {
		// A denied invoke stays in the audit log even though the read path made
		// no other change.
		_ = s.Store.Update(r.Context(), PartitionIAM, func(st *State) error {
			s.audit(st, principal, r, "service.invoke", r.PathValue("resource"), false)
			return nil
		})
		return "", "", appErr
	}
	if c.hasSession {
		_ = s.Store.touchSession(r.Context(), c.sessionHash)
	}
	return host, gatewayID, nil
}

// Keep network I/O outside Store.Update: a slow Worker must not hold the IAM lock.
func (s *Server) invokeWorker(ctx context.Context, host, gatewayID string, in workerInvokeRequest, path *url.URL) (workerInvokeResponse, error) {
	var out workerInvokeResponse
	g, ok := s.Config.Workers.gateway(gatewayID)
	if !ok {
		return out, problem(503, "WORKER_INVOKE_UNAVAILABLE", "Worker gateway not configured")
	}
	target := *path
	if base := g.gatewayBase(); base != "" {
		parsed, e := url.Parse(base)
		if e != nil {
			return out, problem(503, "WORKER_INVOKE_UNAVAILABLE", "Worker gateway not configured")
		}
		target.Scheme = parsed.Scheme
		target.Host = parsed.Host
	} else {
		target.Scheme = "http"
		target.Host = g.inClusterAddress()
	}
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
	for _, name := range []string{"Content-Type", "X-Krun-Release", "X-Krun-Pod"} {
		if value := resp.Header.Get(name); value != "" {
			out.Headers[name] = value
		}
	}
	return out, nil
}
