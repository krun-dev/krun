package control

import (
	"net/http"
	"strconv"

	auditlog "github.com/krun-dev/krun/internal/audit"
)

type auditCursor = auditlog.Cursor
type auditPage = auditlog.Page

func (s *Server) encodeAuditCursor(cursor auditCursor) string { return auditlog.EncodeCursor(cursor) }

func (s *Server) auditList(st *State, p Principal, r *http.Request) (any, int, error) {
	at := now()
	t := systemTarget()
	if r.PathValue("org") != "" {
		var e error
		t, e = s.orgTarget(st, r)
		if e != nil {
			return nil, 0, e
		}
	}
	if e := s.require(st, p, r, "audit.read", t); e != nil {
		return nil, 0, e
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
	out, err := auditlog.List(r.Context(), s.Store, st, t, limit, query.Get("cursor"), at)
	return out, 200, err
}
