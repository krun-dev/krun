package authorization

import (
	cedar "github.com/cedar-policy/cedar-go"
	"github.com/krun-dev/krun/internal/domain"
)

// requestCache belongs to one read-only request and one immutable working copy.
// Never retain the returned Principal across requests or reuse it for mutations.
type requestCache struct {
	state    *domain.State
	revision int
	users    map[string]*cedar.PolicySet
	keys     map[string]*cedar.PolicySet
}

func ForReadSnapshot(st *domain.State, p Principal) Principal {
	p.cache = &requestCache{state: st, revision: st.Revision, users: map[string]*cedar.PolicySet{}, keys: map[string]*cedar.PolicySet{}}
	return p
}
