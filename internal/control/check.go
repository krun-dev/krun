package control

import (
	"net/http"
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
	d, e := Authorize(st, p, in.Action, t)
	if e != nil {
		return nil, 0, e
	}
	if !d.Allowed {
		readAction := t.Kind + ".read"
		if t.Kind == "api_key" {
			readAction = "apiKey.read"
		}
		if t.Kind == "system" {
			readAction = "system.role.read"
		}
		visible, err := Authorize(st, p, readAction, t)
		if err != nil {
			return nil, 0, err
		}
		if !visible.Allowed {
			return nil, 0, notFound()
		}
	}
	s.audit(st, p, r, in.Action, t.ID, d.Allowed)
	return d, 200, nil
}
