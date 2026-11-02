package control

import (
	"net/http"

	"github.com/krun-dev/krun/internal/iam"
)

func (s *Server) orgList(st *State, p Principal, r *http.Request) (any, int, error) {
	out, err := s.iam().Organizations(st, p)
	if err != nil {
		return nil, 0, err
	}
	return items(out)
}

func (s *Server) orgCreate(st *State, p Principal, r *http.Request) (any, int, error) {
	// Self-service still requires an authenticated browser or CLI session; API
	// keys are excluded so a leaked key cannot mint new organizations.
	if !s.Config.OrganizationSelfService {
		if e := s.require(st, p, r, "organization.create", systemTarget()); e != nil {
			return nil, 0, e
		}
	} else if p.Kind != "session" && p.Kind != "cli_session" {
		return nil, 0, forbidden()
	}
	var in iam.NameInput
	if e := body(r, &in); e != nil {
		return nil, 0, e
	}
	out, err := s.iam().CreateOrganization(st, p, in)
	return out, 201, err
}

func (s *Server) orgDetail(st *State, p Principal, r *http.Request) (any, int, error) {
	t, e := s.orgTarget(st, r)
	if e != nil {
		return nil, 0, e
	}
	if e = s.require(st, p, r, "organization.read", t); e != nil {
		if denied, ok := e.(*APIError); ok && denied.Status == 403 {
			return nil, 0, notFound()
		}
		return nil, 0, e
	}
	return st.Organizations[t.ID], 200, nil
}

func (s *Server) orgUpdate(st *State, p Principal, r *http.Request) (any, int, error) {
	t, e := s.orgTarget(st, r)
	if e != nil {
		return nil, 0, e
	}
	if e = s.require(st, p, r, "organization.update", t); e != nil {
		return nil, 0, e
	}
	var in iam.OrganizationInput
	if e = body(r, &in); e != nil {
		return nil, 0, e
	}
	out, err := s.iam().UpdateOrganization(st, t, in)
	return out, 200, err
}
