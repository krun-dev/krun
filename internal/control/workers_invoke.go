package control

import (
	"context"
	"net/http"
	"net/url"

	invocation "github.com/krun-dev/krun/internal/adapters/invoke"
)

type workerInvokeRequest = invocation.Request
type workerInvokeResponse = invocation.Response

const workerInvokePathLimit = invocation.InvokePathLimit
const workerInvokeBodyLimit = invocation.InvokeBodyLimit
const workerInvokeResponseLimit = invocation.InvokeResponseLimit
const workerInvokeTimeout = invocation.InvokeTimeout
const workerInvokeInvalidMessage = invocation.InvokeInvalidMessage

var newWorkerInvokeClient = invocation.NewHTTPClient

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
	path, err := in.Validate()
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

// workerInvokeTarget resolves the upstream host for an invoke call. It reads
// both partitions because authorization needs IAM while routing needs Workers,
// and it writes nothing: a denial is audited separately.
func (s *Server) workerInvokeTarget(r *http.Request) (string, string, error) {
	c, e := s.readCredentials(r)
	if e != nil {
		return "", "", e
	}
	r, e = s.scopedRequest(r, c)
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
		host, gatewayID, appErr = s.workerApplication(r).ResolveInvocation(st, principal, workerRef(r))
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
		_ = s.Store.TouchSession(r.Context(), c.sessionHash)
	}
	return host, gatewayID, nil
}

// Keep network I/O outside Store.Update: a slow Worker must not hold the IAM lock.
func (s *Server) invokeWorker(ctx context.Context, host, gatewayID string, in workerInvokeRequest, path *url.URL) (workerInvokeResponse, error) {
	return (&invocation.Client{Config: s.Config.Workers, HTTP: s.workerInvokeClient}).Call(ctx, host, gatewayID, in, path)
}
