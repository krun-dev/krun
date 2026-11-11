package authorization

import (
	"slices"

	"github.com/krun-dev/krun/internal/domain"
)

func ValidatePolicy(st *domain.State, p Principal, org, scope string, policy domain.Policy) error {
	if policy.Version != 1 || len(policy.Statements) == 0 || len(policy.Statements) > 32 {
		return domain.Invalid("POLICY_INVALID", "schema_version 1 and 1–32 statements required")
	}
	for _, s := range policy.Statements {
		r := s.Resource
		if s.Effect != "allow" && s.Effect != "deny" || len(s.Actions) == 0 || len(s.Actions) > 64 {
			return domain.Invalid("POLICY_INVALID", "invalid effect or actions")
		}
		if r.AllSystem {
			if scope != "system" || len(r.IDs) > 0 || r.All || r.Organization != "" || !slices.Contains([]string{"*", "system", "organization", "worker", "model", "api_key"}, r.Kind) {
				return domain.Invalid("POLICY_INVALID", "cross-workspace wildcard requires a system-scoped key and no organization or IDs")
			}
		} else {
			if scope == "organization" && r.Organization != org {
				return domain.Invalid("POLICY_INVALID", "selector must belong to this organization")
			}
			if r.All == (len(r.IDs) > 0) || !slices.Contains([]string{"*", "organization", "worker", "model", "api_key"}, r.Kind) || (r.Kind == "*" && !r.All) {
				return domain.Invalid("POLICY_INVALID", "choose exact IDs or organization wildcard")
			}
			if r.All && (r.Organization == "" || !SourceAllowed(st, p.User, r.Organization)) {
				return domain.Invalid("POLICY_INVALID", "wildcard requires an accessible organization")
			}
		}
		envs := s.Conditions.Environments
		if len(envs) == 0 {
			envs = environments
		}
		for _, env := range envs {
			if !EnvironmentValid(env) {
				return domain.Invalid("POLICY_INVALID", "unknown environment")
			}
		}
		for _, action := range s.Actions {
			if action != "*" && (len(actions[action]) == 0 || (r.Kind != "*" && !slices.Contains(actions[action], r.Kind))) {
				return domain.Invalid("POLICY_INVALID", "unknown action or incompatible resource kind")
			}
		}
		// Policies are ceilings, including exact selectors. The owner's current
		// permissions are evaluated by Authorize on every request.
		for _, id := range r.IDs {
			t, e := Resolve(st, r.Kind, id, "global")
			if e != nil || t.Organization != r.Organization {
				return domain.Invalid("POLICY_INVALID", "invalid resource ownership")
			}
		}
	}
	_, err := compileKey(domain.APIKey{ID: "validation", Policy: policy})
	return err
}
