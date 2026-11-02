package iam

import (
	"time"

	"github.com/krun-dev/krun/internal/authorization"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/securetoken"
)

func (s Service) CreateKey(st *domain.State, p authorization.Principal, t authorization.Target, in KeyInput) (CreatedKey, error) {
	if !nameValid(in.Name) || in.Expires == nil || *in.Expires < 0 || (*in.Expires > 0 && *in.Expires < 60) || *in.Expires > 90*86400 {
		return CreatedKey{}, domain.Problem(422, "POLICY_INVALID", "Name and expires_in 0 (never) or between 60 seconds and 90 days required")
	}
	org := t.Organization
	scope := t.Kind
	if e := authorization.ValidatePolicy(st, p, org, scope, in.Policy); e != nil {
		return CreatedKey{}, e
	}
	id := securetoken.NewID("key")
	secret := "krun_key_" + securetoken.Random()
	var expires *time.Time
	if *in.Expires > 0 {
		deadline := time.Now().UTC().Add(time.Duration(*in.Expires) * time.Second)
		expires = &deadline
	}
	k := domain.APIKey{ID: id, Owner: p.User.ID, Organization: org, ScopeKind: scope, Name: in.Name, Prefix: secret[:17], Policy: in.Policy, Expires: expires, Revision: 1}
	st.Keys[id] = k
	st.KeyHashes[id] = securetoken.Digest(secret)
	st.Revision++
	return CreatedKey{ID: id, Key: secret, Metadata: k, PolicyRevision: k.Revision, Expires: k.Expires}, nil
}

func (s Service) Keys(st *domain.State, p authorization.Principal, org string) ([]domain.APIKey, error) {
	if org != "" {
		if _, ok := st.Organizations[org]; !ok {
			return nil, domain.NotFound()
		}
	}
	out := []domain.APIKey{}
	for _, k := range st.Keys {
		if k.Organization != org {
			continue
		}
		t, _ := authorization.Resolve(st, "api_key", k.ID, "global")
		d, e := authorization.Authorize(st, p, "apiKey.read", t)
		if e != nil {
			return nil, e
		}
		if d.Allowed {
			out = append(out, k)
		}
	}
	return out, nil
}

func (s Service) UpdateKey(st *domain.State, p authorization.Principal, t authorization.Target, in KeyUpdateInput) (domain.APIKey, error) {
	k, err := s.ActiveKey(st, t.ID)
	if err != nil {
		return domain.APIKey{}, err
	}
	if in.Revision != k.Revision {
		return domain.APIKey{}, domain.Problem(409, "REVISION_CONFLICT", "Key changed")
	}
	if !nameValid(in.Name) {
		return domain.APIKey{}, domain.Problem(422, "INVALID_REQUEST", "Name required")
	}
	if e := authorization.ValidatePolicy(st, p, k.Organization, k.ScopeKind, in.Policy); e != nil {
		return domain.APIKey{}, e
	}
	owner := st.Users[k.Owner]
	if owner.Status != "active" || !s.sourceEnabled(owner.Source) {
		return domain.APIKey{}, domain.Problem(422, "POLICY_INVALID", "Owner inactive")
	}
	if e := authorization.ValidatePolicy(st, authorization.Principal{User: owner, Kind: "session"}, k.Organization, k.ScopeKind, in.Policy); e != nil {
		return domain.APIKey{}, e
	}
	k.Name = in.Name
	k.Policy = in.Policy
	k.Revision++
	st.Keys[k.ID] = k
	st.Revision++
	return k, nil
}

func (s Service) RevokeKey(st *domain.State, t authorization.Target) error {
	k := st.Keys[t.ID]
	k.Revoked = true
	k.Revision++
	st.Keys[k.ID] = k
	st.Revision++
	return nil
}
