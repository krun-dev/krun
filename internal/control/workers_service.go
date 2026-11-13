package control

import (
	"io"
	"net/http"
	"net/url"
	"sync/atomic"

	"github.com/krun-dev/krun/internal/adapters/runtimeclient"
)

var serviceInvocationCount atomic.Uint64
var serviceInvocationSlots = make(chan struct{}, 128)

func (s *Server) workerRuntimeServiceInvoke(w http.ResponseWriter, r *http.Request) {
	source, allowed := s.workerRuntimeServingPool(r)
	if !allowed {
		s.writeError(w, r, forbidden())
		return
	}
	target, err := s.workerApplication(r).ResolveServiceInvocation(r.Context(), r.PathValue("caller"), r.PathValue("binding"), source.ID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	requestURL := r.Header.Get("X-Krun-Service-URL")
	original, err := url.Parse(requestURL)
	if err != nil || len(requestURL) > 2048 || original.Host == "" || original.User != nil ||
		(original.Scheme != "http" && original.Scheme != "https") {
		s.writeError(w, r, problem(400, "INVALID_REQUEST", "Invalid service request URL"))
		return
	}
	targetPool, ok := s.Config.Workers.RuntimePool(target.Pool)
	if !ok || s.workers == nil {
		s.writeError(w, r, problem(503, "WORKER_UNAVAILABLE", "Service target runtime is unavailable"))
		return
	}
	select {
	case serviceInvocationSlots <- struct{}{}:
		defer func() { <-serviceInvocationSlots }()
	default:
		s.writeError(w, r, problem(503, "WORKER_UNAVAILABLE", "Service invocation capacity reached"))
		return
	}
	members, err := s.workers.Kube.RuntimeMembers(r.Context(), targetPool.ID, targetPool.RuntimeService)
	if err != nil || len(members) == 0 {
		s.writeError(w, r, problem(503, "WORKER_UNAVAILABLE", "Service target runtime is unavailable"))
		return
	}
	member := members[serviceInvocationCount.Add(1)%uint64(len(members))]
	body := http.MaxBytesReader(w, r.Body, 32<<20)
	response, err := runtimeclient.CallService(r.Context(), s.workers.Http, targetPool, member.IP, target.Host, target.Version, requestURL, r.Method, r.Header, body)
	if err != nil {
		s.writeError(w, r, problem(503, "WORKER_UNAVAILABLE", "Service invocation failed"))
		return
	}
	defer response.Body.Close()
	runtimeclient.StripHopHeaders(response.Header)
	for name, values := range response.Header {
		for _, value := range values {
			w.Header().Add(name, value)
		}
	}
	w.WriteHeader(response.StatusCode)
	controller := http.NewResponseController(w)
	if err := controller.Flush(); err != nil {
		_, _ = io.Copy(w, response.Body)
		return
	}
	buffer := make([]byte, 32<<10)
	for {
		n, err := response.Body.Read(buffer)
		if n > 0 {
			if _, err := w.Write(buffer[:n]); err != nil {
				return
			}
			if err := controller.Flush(); err != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func (s *Server) workerRuntimeServiceDescriptor(w http.ResponseWriter, r *http.Request) {
	pool, allowed := s.workerRuntimeServingPool(r)
	if !allowed {
		s.writeError(w, r, forbidden())
		return
	}
	version, _, err := s.Store.RuntimeVersion(r.Context(), r.PathValue("version"), pool.ID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	for name, binding := range version.ServiceBindings {
		binding.GrantedBy = nil
		version.ServiceBindings[name] = binding
	}
	w.Header().Set("Cache-Control", "no-store")
	s.json(w, 200, map[string]any{"version": version})
}
