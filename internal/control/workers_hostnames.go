package control

import (
	"net/http"
	"strconv"

	workerapp "github.com/krun-dev/krun/internal/workers"
)

func (s *Server) workerHostnameCatalog(st *State, p Principal, r *http.Request) (any, int, error) {
	out, result, err := s.workerApplication(r).ManagedHostnameCatalog(r.Context(), st, p, workerRef(r))
	return out, workerHTTPStatus(result), err
}

func (s *Server) workerManagedHostnames(st *State, p Principal, r *http.Request) (any, int, error) {
	out, result, err := s.workerApplication(r).WorkerManagedHostnames(r.Context(), st, p, workerRef(r))
	return out, workerHTTPStatus(result), err
}

func (s *Server) workerSaveHostname(st *State, p Principal, r *http.Request) (any, int, error) {
	var in workerapp.ManagedHostnameInput
	retire := r.Method == http.MethodDelete
	if retire {
		in.ExpectedRevision, _ = strconv.Atoi(r.URL.Query().Get("revision"))
	} else if err := bodyWithLimit(r, &in, apiJSONBodyMax); err != nil {
		return nil, 0, err
	}
	out, result, err := s.workerApplication(r).SaveManagedHostname(r.Context(), st, p, workerRef(r), r.PathValue("host"), in, retire)
	return out, workerHTTPStatus(result), err
}
