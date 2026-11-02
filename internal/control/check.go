package control

import (
	"net/http"

	"github.com/krun-dev/krun/internal/authorization"
)

func (s *Server) check(st *State, p Principal, r *http.Request) (any, int, error) {
	var in struct {
		Action      string `json:"action"`
		Kind        string `json:"resource_kind"`
		ID          string `json:"resource_id"`
		Environment string `json:"environment"`
	}
	if e := body(r, &in); e != nil {
		return nil, 0, e
	}
	t, e := resolve(st, in.Kind, in.ID, in.Environment)
	if e != nil {
		return nil, 0, e
	}
	d, e := authorization.Check(st, p, in.Action, t)
	if e != nil {
		return nil, 0, e
	}
	s.audit(st, p, r, in.Action, t.ID, d.Allowed)
	return d, 200, nil
}
