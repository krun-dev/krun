package control

import (
	"net/http"
	"net/url"
	"strconv"

	auditlog "github.com/krun-dev/krun/internal/audit"
	"github.com/krun-dev/krun/internal/authorization"
)

type auditCursor = auditlog.Cursor
type auditPage = auditlog.Page

func (s *Server) auditTarget(st *State, p Principal, r *http.Request) (authorization.Target, error) {
	t := systemTarget()
	if r.PathValue("org") != "" {
		var e error
		t, e = s.orgTarget(st, r)
		if e != nil {
			return authorization.Target{}, e
		}
	}
	if e := s.require(st, p, r, "audit.read", t); e != nil {
		return authorization.Target{}, e
	}
	return t, nil
}

func (s *Server) auditList(st *State, p Principal, r *http.Request) (any, int, error) {
	target, err := s.auditTarget(st, p, r)
	if err != nil {
		return nil, 0, err
	}
	query := r.URL.Query()
	limit := 50
	if values, ok := query["limit"]; ok {
		var err error
		if len(values) != 1 {
			return nil, 0, problem(400, "INVALID_REQUEST", "Use one limit")
		}
		limit, err = strconv.Atoi(values[0])
		if err != nil || limit < 1 || limit > 500 {
			return nil, 0, problem(400, "INVALID_REQUEST", "Audit limit must be between 1 and 500")
		}
	}
	if len(query["cursor"]) > 1 {
		return nil, 0, problem(400, "INVALID_REQUEST", "Use one cursor")
	}
	out, err := auditlog.List(r.Context(), s.Store, target, limit, query.Get("cursor"), now())
	return out, 200, err
}

type auditCSVResponse []byte

func (s *Server) auditExport(st *State, p Principal, r *http.Request) (any, int, error) {
	target, err := s.auditTarget(st, p, r)
	if err != nil {
		return nil, 0, err
	}

	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, 0, problem(400, "INVALID_REQUEST", "Invalid audit export query")
	}
	if len(query["cursor"]) > 1 {
		return nil, 0, problem(400, "INVALID_REQUEST", "Use one cursor")
	}
	redact := true
	if values, ok := query["redact"]; ok {
		if len(values) != 1 || (values[0] != "true" && values[0] != "false") {
			return nil, 0, problem(400, "INVALID_REQUEST", "Audit redact must be true or false")
		}
		redact = values[0] == "true"
	}

	data, err := auditlog.ExportCSV(r.Context(), s.Store, target, query.Get("cursor"), now(), redact)
	return auditCSVResponse(data), http.StatusOK, err
}
