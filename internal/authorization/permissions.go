package authorization

import (
	"slices"

	"github.com/krun-dev/krun/internal/domain"
)

// OrganizationPermissions uses the same policy engine as individual requests,
// including group membership, identity-source restrictions and key policies.
func OrganizationPermissions(st *domain.State, p Principal, target Target) (map[string]bool, error) {
	out := make(map[string]bool)
	for action, kinds := range actions {
		if !slices.Contains(kinds, "organization") {
			continue
		}
		decision, err := Authorize(st, p, action, target)
		if err != nil {
			return nil, err
		}
		out[action] = decision.Allowed
	}
	return out, nil
}
