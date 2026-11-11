package authorization

import (
	"strings"

	"github.com/krun-dev/krun/internal/domain"
)

// ResolveReference converts a public resource path into the immutable
// identity used by Cedar and stored grants.
func ResolveReference(st *domain.State, kind, ref, env string) (Target, error) {
	if target, err := Resolve(st, kind, ref, env); err == nil {
		return target, nil
	}
	if kind == "organization" {
		if id := organizationByShortName(st, ref); id != "" {
			return Resolve(st, kind, id, env)
		}
	}
	if kind != "worker" && kind != "model" {
		return Target{}, domain.NotFound()
	}
	parts := strings.Split(ref, "/")
	if len(parts) != 3 || parts[1] != kind+"s" || parts[2] == "" {
		return Target{}, domain.NotFound()
	}
	org := organizationByShortName(st, parts[0])
	for _, resource := range st.Resources {
		if resource.Organization == org && resource.Kind == kind && resource.Locator == parts[2] {
			return Resolve(st, kind, resource.ID, env)
		}
	}
	return Target{}, domain.NotFound()
}

func organizationByShortName(st *domain.State, shortName string) string {
	if shortName != "" {
		for _, org := range st.Organizations {
			if org.ShortName == shortName {
				return org.ID
			}
		}
	}
	return ""
}

// ResolvePolicyReferences stores IDs so a deleted resource cannot transfer
// its permissions to a later resource that takes the same path.
func ResolvePolicyReferences(st *domain.State, policy *domain.Policy) error {
	for i := range policy.Statements {
		selector := &policy.Statements[i].Resource
		if selector.Organization != "" {
			org, err := ResolveReference(st, "organization", selector.Organization, "global")
			if err != nil {
				return domain.Invalid("POLICY_INVALID", "Unknown organization")
			}
			selector.Organization = org.ID
		}
		for j, ref := range selector.IDs {
			target, err := ResolveReference(st, selector.Kind, ref, "global")
			if err != nil {
				return domain.Invalid("POLICY_INVALID", "Invalid resource ownership")
			}
			selector.IDs[j] = target.ID
		}
	}
	return nil
}
