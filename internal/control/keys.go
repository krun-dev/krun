package control

import (
	"net/http"
	"time"
)

func (s *Server) keyCreate(st *State, p Principal, r *http.Request) (any, int, error) {
	t, e := s.scopeTarget(st, r, "grant")
	if e != nil {
		return nil, 0, e
	}
	if e = s.require(st, p, r, "apiKey.create", t); e != nil {
		return nil, 0, e
	}
	var in struct {
		Name    string `json:"name"`
		Expires *int   `json:"expires_in"`
		Policy  Policy `json:"policy"`
	}
	if e = body(r, &in); e != nil {
		return nil, 0, e
	}
	if !nameValid(in.Name) || in.Expires == nil || *in.Expires < 0 || (*in.Expires > 0 && *in.Expires < 60) || *in.Expires > 90*86400 {
		return nil, 0, problem(422, "POLICY_INVALID", "Name and expires_in 0 (never) or between 60 seconds and 90 days required")
	}
	org := t.Organization
	scope := t.Kind
	if e = validatePolicy(st, p, org, scope, in.Policy); e != nil {
		return nil, 0, e
	}
	id := randomID("key")
	secret := "krun_key_" + randomToken()
	var expires *time.Time
	if *in.Expires > 0 {
		deadline := now().Add(time.Duration(*in.Expires) * time.Second)
		expires = &deadline
	}
	k := APIKey{ID: id, Owner: p.User.ID, Organization: org, ScopeKind: scope, Name: in.Name, Prefix: secret[:17], Policy: in.Policy, Expires: expires, Revision: 1}
	st.Keys[id] = k
	st.KeyHashes[id] = digest(secret)
	st.Revision++
	return map[string]any{"id": id, "key": secret, "metadata": k, "policy_revision": k.Revision, "expires_at": k.Expires}, 201, nil
}
func (s *Server) keyList(st *State, p Principal, r *http.Request) (any, int, error) {
	org := r.PathValue("org")
	if org != "" {
		if _, ok := st.Organizations[org]; !ok {
			return nil, 0, notFound()
		}
	}
	out := []APIKey{}
	for _, k := range st.Keys {
		if k.Organization != org {
			continue
		}
		t, _ := resolve(st, "api_key", k.ID, "global")
		d, e := Authorize(st, p, "apiKey.read", t)
		if e != nil {
			return nil, 0, e
		}
		if d.Allowed {
			out = append(out, k)
		}
	}
	return items(out)
}
func (s *Server) keyDetail(st *State, p Principal, r *http.Request) (any, int, error) {
	t, e := resolve(st, "api_key", r.PathValue("key"), "global")
	if e != nil {
		return nil, 0, e
	}
	if e = s.require(st, p, r, "apiKey.read", t); e != nil {
		if denied, ok := e.(*APIError); ok && denied.Status == 403 {
			return nil, 0, notFound()
		}
		return nil, 0, e
	}
	return st.Keys[t.ID], 200, nil
}
func (s *Server) keyUpdate(st *State, p Principal, r *http.Request) (any, int, error) {
	t, e := resolve(st, "api_key", r.PathValue("key"), "global")
	if e != nil {
		return nil, 0, e
	}
	if e = s.require(st, p, r, "apiKey.update", t); e != nil {
		return nil, 0, e
	}
	k := st.Keys[t.ID]
	if !k.active() {
		return nil, 0, problem(409, "KEY_INACTIVE", "Key inactive")
	}
	var in struct {
		Name     string `json:"name"`
		Policy   Policy `json:"policy"`
		Revision int    `json:"expected_revision"`
	}
	if e = body(r, &in); e != nil {
		return nil, 0, e
	}
	if in.Revision != k.Revision {
		return nil, 0, problem(409, "REVISION_CONFLICT", "Key changed")
	}
	if !nameValid(in.Name) {
		return nil, 0, problem(422, "INVALID_REQUEST", "Name required")
	}
	if e = validatePolicy(st, p, k.Organization, k.ScopeKind, in.Policy); e != nil {
		return nil, 0, e
	}
	owner := st.Users[k.Owner]
	if owner.Status != "active" || !s.sourceEnabled(owner.Source) {
		return nil, 0, problem(422, "POLICY_INVALID", "Owner inactive")
	}
	if e = validatePolicy(st, Principal{User: owner, Kind: "session"}, k.Organization, k.ScopeKind, in.Policy); e != nil {
		return nil, 0, e
	}
	k.Name = in.Name
	k.Policy = in.Policy
	k.Revision++
	st.Keys[k.ID] = k
	st.Revision++
	return k, 200, nil
}
func (s *Server) keyDelete(st *State, p Principal, r *http.Request) (any, int, error) {
	t, e := resolve(st, "api_key", r.PathValue("key"), "global")
	if e != nil {
		return nil, 0, e
	}
	if e = s.require(st, p, r, "apiKey.revoke", t); e != nil {
		return nil, 0, e
	}
	k := st.Keys[t.ID]
	k.Revoked = true
	k.Revision++
	st.Keys[k.ID] = k
	st.Revision++
	return nil, 204, nil
}
