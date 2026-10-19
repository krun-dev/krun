package control

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	cedar "github.com/cedar-policy/cedar-go"
)

type Principal struct {
	User        User
	Kind        string
	Key         *APIKey
	SessionHash string
}
type Target struct{ Kind, ID, Organization, Owner, Environment string }
type Decision struct {
	Allowed     bool   `json:"allowed"`
	UserAllowed bool   `json:"user_allowed"`
	KeyAllowed  bool   `json:"key_allowed"`
	Revision    int    `json:"revision"`
	Reason      string `json:"reason"`
}

var actions = map[string][]string{
	"organization.create": {"system"}, "system.configure": {"system"}, "system.role.read": {"system"}, "system.role.grant": {"system"}, "system.role.revoke": {"system"}, "system.user.read": {"system"}, "system.user.update": {"system"}, "audit.read": {"system", "organization"},
	"organization.read": {"organization"}, "organization.update": {"organization"}, "organization.delete": {"organization"}, "member.read": {"organization"}, "member.manage": {"organization"}, "iam.read": {"organization"}, "iam.grant": {"organization"}, "iam.revoke": {"organization"},
	"apiKey.create": {"organization", "system"}, "apiKey.read": {"api_key"}, "apiKey.update": {"api_key"}, "apiKey.revoke": {"api_key"},
	"service.list": {"organization"}, "service.create": {"organization"}, "service.read": {"service"}, "service.update": {"service"}, "service.delete": {"service"}, "service.publish": {"service"}, "service.rollback": {"service"}, "service.version.read": {"service"}, "service.version.create": {"service"}, "service.invoke": {"service"},
	"model.list": {"organization"}, "model.create": {"organization"}, "model.read": {"model"}, "model.update": {"model"}, "model.delete": {"model"}, "model.publish": {"model"}, "model.rollback": {"model"}, "model.version.read": {"model"}, "model.version.create": {"model"}, "model.use": {"model"}, "model.invoke": {"model"}, "build.start": {"service", "model"}, "log.read": {"service", "model"}, "source.read": {"service", "model"}, "binding.attach": {"service"}, "binding.detach": {"service"},
}
var roleActions = map[string][]string{
	"viewer":    {"organization.read", "service.list", "service.read", "model.list", "model.read"},
	"developer": {"organization.read", "service.list", "service.read", "service.create", "service.update", "service.version.create", "service.version.read", "model.list", "model.read", "model.create", "model.update", "model.version.create", "model.version.read", "build.start", "log.read", "source.read", "service.invoke"},
	"publisher": {"organization.read", "service.list", "service.read", "service.version.read", "service.publish", "service.rollback", "service.invoke", "model.list", "model.read", "model.version.read", "model.publish", "model.rollback"},
}
// Environment values: the reserved scopes ("global", "development") plus the
// baseline and any preview slug a service may define.
var environments = []string{"global", "development", "production"}

func environmentValid(v string) bool {
	return contains(environments, v) || (v != "test" && v != "staging" && workerPreviewValid(v))
}

func entityType(kind string) string {
	switch kind {
	case "api_key":
		return "ApiKey"
	case "system":
		return "System"
	case "organization":
		return "Organization"
	case "service":
		return "Service"
	case "model":
		return "Model"
	}
	return "Unknown"
}
func uid(kind, id string) string { return entityType(kind) + "::" + strconv.Quote(id) }
func stringSet(values []string) string {
	v := []string{}
	for _, x := range values {
		v = append(v, strconv.Quote(x))
	}
	return "[" + strings.Join(v, ",") + "]"
}
func roleMatches(st *State, a Assignment, u User) bool {
	if a.SubjectKind == "user" {
		return a.SubjectID == u.ID
	}
	g, ok := st.Groups[a.SubjectID]
	return ok && ((g.ScopeKind == "system" && a.ScopeKind == "system") || (g.ScopeKind == "organization" && a.ScopeKind != "system" && g.ScopeID == scopeOrg(st, a))) && contains(g.Members, u.ID)
}
func scopeOrg(st *State, a Assignment) string {
	if a.ScopeKind == "organization" {
		return a.ScopeID
	}
	return st.Resources[a.ScopeID].Organization
}
func isAdmin(st *State, u User, org string) bool {
	if u.Status != "active" {
		return false
	}
	for _, a := range st.Assignments {
		if !roleMatches(st, a, u) {
			continue
		}
		if a.Role == "system_admin" && a.ScopeKind == "system" {
			return true
		}
		if org != "" && a.Role == "org_admin" && a.ScopeKind == "organization" && a.ScopeID == org {
			if _, ok := st.Members[memberKey(org, u.ID)]; ok {
				return true
			}
		}
	}
	return false
}
func sourceAllowed(st *State, u User, org string) bool {
	if org == "" {
		return true
	}
	o, ok := st.Organizations[org]
	return ok && (len(o.Sources) == 0 || contains(o.Sources, u.Source))
}
func addPolicy(ps *cedar.PolicySet, id, body string) error {
	var p cedar.Policy
	if e := p.UnmarshalCedar([]byte(body)); e != nil {
		return e
	}
	ps.Add(cedar.PolicyID(id), &p)
	return nil
}
func compileUser(st *State, u User) (*cedar.PolicySet, error) {
	ps := cedar.NewPolicySet()
	principal := "principal == User::" + strconv.Quote(u.ID)
	for _, a := range st.Assignments {
		if !roleMatches(st, a, u) {
			continue
		}
		resource := "resource"
		condition := "true"
		list := []string{}
		if a.Role == "system_admin" && a.ScopeKind == "system" {
			for action := range actions {
				list = append(list, action)
			}
		} else {
			org := scopeOrg(st, a)
			if _, ok := st.Members[memberKey(org, u.ID)]; !ok || !sourceAllowed(st, u, org) {
				continue
			}
			if a.ScopeKind == "organization" {
				resource = "resource in " + uid("organization", org)
			} else {
				resource = "resource == " + uid(a.ScopeKind, a.ScopeID)
			}
			if a.Role == "org_admin" {
				for action, kinds := range actions {
					if !strings.HasPrefix(action, "system.") && action != "organization.create" && !contains(kinds, "system") {
						list = append(list, action)
					}
					if action == "apiKey.create" {
						list = append(list, action)
					}
				}
			} else {
				list = roleActions[a.Role]
			}
		}
		if len(a.Conditions.Environments) > 0 {
			condition = stringSet(a.Conditions.Environments) + ".contains(context.environment)"
		}
		for _, action := range list {
			actionResource := resource
			if (a.ScopeKind == "service" || a.ScopeKind == "model") && (action == a.ScopeKind+".list" || action == "organization.read") {
				actionResource = "resource == " + uid("organization", scopeOrg(st, a))
			}
			body := fmt.Sprintf("permit (%s, action == Action::%q, %s) when { %s };", principal, action, actionResource, condition)
			if e := addPolicy(ps, a.ID+action, body); e != nil {
				return nil, e
			}
		}
	}
	// Every user can manage personal cross-workspace credentials. These grants do
	// not confer access to other users' keys or to any organization resource.
	if e := addPolicy(ps, "personal-create", fmt.Sprintf("permit (%s, action == Action::\"apiKey.create\", resource == %s);", principal, uid("system", "system"))); e != nil {
		return nil, e
	}
	for _, action := range []string{"apiKey.read", "apiKey.update", "apiKey.revoke"} {
		if e := addPolicy(ps, "personal-"+action, fmt.Sprintf("permit (%s, action == Action::%q, resource) when { resource.kind == \"api_key\" && resource.owner == %q && resource.organization == \"\" };", principal, action, u.ID)); e != nil {
			return nil, e
		}
	}
	// Membership grants only management of the user's own restricted credentials.
	for _, m := range st.Members {
		if m.User != u.ID || !sourceAllowed(st, u, m.Organization) {
			continue
		}
		if e := addPolicy(ps, "self-create-"+m.Organization, fmt.Sprintf("permit (%s, action == Action::\"apiKey.create\", resource == %s);", principal, uid("organization", m.Organization))); e != nil {
			return nil, e
		}
		for _, action := range []string{"apiKey.read", "apiKey.update", "apiKey.revoke"} {
			if e := addPolicy(ps, "self-"+m.Organization+action, fmt.Sprintf("permit (%s, action == Action::%q, resource) when { resource has owner && resource.owner == %q && resource has organization && resource.organization == %q };", principal, action, u.ID, m.Organization)); e != nil {
				return nil, e
			}
		}
	}
	return ps, nil
}

// Expand action wildcards against the registered actions for this selector.
// Authorization still intersects the resulting policy with the owner's roles.
func statementActions(s Statement) []string {
	set := map[string]bool{}
	for _, action := range s.Actions {
		if action != "*" {
			set[action] = true
			continue
		}
		for name, kinds := range actions {
			if s.Resource.Kind == "*" || (s.Resource.AllSystem && s.Resource.Kind == "system") || contains(kinds, s.Resource.Kind) {
				set[name] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for name := range set {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func compileKey(k APIKey) (*cedar.PolicySet, error) {
	ps := cedar.NewPolicySet()
	for i, s := range k.Policy.Statements {
		effect := "permit"
		if s.Effect == "deny" {
			effect = "forbid"
		}
		selector := s.Resource
		parts := []string{"resource.kind == " + strconv.Quote(selector.Kind)}
		if selector.Kind == "*" {
			parts = []string{"true"}
		}
		if selector.AllSystem {
			// Keep the original system selector compatible: it denotes all kinds.
			if selector.Kind == "system" {
				parts = []string{"true"}
			}
		} else {
			if selector.Organization != "" {
				parts = append(parts, "resource.organization == "+strconv.Quote(selector.Organization))
			}
			if !selector.All {
				ids := []string{}
				for _, id := range selector.IDs {
					ids = append(ids, uid(selector.Kind, id))
				}
				parts = append(parts, "["+strings.Join(ids, ",")+"].contains(resource)")
			}
		}
		if len(s.Conditions.Environments) > 0 {
			parts = append(parts, stringSet(s.Conditions.Environments)+".contains(context.environment)")
		}
		for _, action := range statementActions(s) {
			body := fmt.Sprintf("%s (principal == ApiKey::%q, action == Action::%q, resource) when { %s };", effect, k.ID, action, strings.Join(parts, " && "))
			if e := addPolicy(ps, fmt.Sprintf("key-%d-%s", i, action), body); e != nil {
				return nil, e
			}
		}
	}
	return ps, nil
}
func evaluate(ps *cedar.PolicySet, kind, id, action string, t Target) (bool, error) {
	parents := []any{}
	if t.Organization != "" && t.Kind != "organization" {
		parents = append(parents, map[string]string{"type": "Organization", "id": t.Organization})
	}
	list := []any{map[string]any{"uid": map[string]string{"type": entityType(t.Kind), "id": t.ID}, "parents": parents, "attrs": map[string]string{"kind": t.Kind, "organization": t.Organization, "owner": t.Owner}}}
	if t.Organization != "" && t.Kind != "organization" {
		list = append(list, map[string]any{"uid": map[string]string{"type": "Organization", "id": t.Organization}, "parents": []any{}, "attrs": map[string]string{"kind": "organization", "organization": t.Organization, "owner": ""}})
	}
	raw, _ := json.Marshal(list)
	var entities cedar.EntityMap
	if e := json.Unmarshal(raw, &entities); e != nil {
		return false, e
	}
	req := cedar.Request{Principal: cedar.NewEntityUID(cedar.EntityType(kind), cedar.String(id)), Action: cedar.NewEntityUID("Action", cedar.String(action)), Resource: cedar.NewEntityUID(cedar.EntityType(entityType(t.Kind)), cedar.String(t.ID))}
	raw, _ = json.Marshal(map[string]string{"environment": t.Environment})
	if e := json.Unmarshal(raw, &req.Context); e != nil {
		return false, e
	}
	decision, diag := cedar.Authorize(ps, entities, req)
	if len(diag.Errors) > 0 {
		return false, errors.New("cedar evaluation failed")
	}
	return decision == cedar.Allow, nil
}
func Authorize(st *State, p Principal, action string, t Target) (Decision, error) {
	d := Decision{Revision: st.Revision, KeyAllowed: p.Key == nil, Reason: "PERMISSION_DENIED"}
	if !contains(actions[action], t.Kind) || !environmentValid(t.Environment) || p.User.Status != "active" || !sourceAllowed(st, p.User, t.Organization) {
		return d, nil
	}
	ps, e := compileUser(st, p.User)
	if e != nil {
		return d, e
	}
	d.UserAllowed, e = evaluate(ps, "User", p.User.ID, action, t)
	if e != nil {
		return d, e
	}
	if p.Key != nil {
		if p.Key.ScopeKind == "organization" && p.Key.Organization != t.Organization {
			return d, nil
		}
		ps, e = compileKey(*p.Key)
		if e != nil {
			return d, e
		}
		d.KeyAllowed, e = evaluate(ps, "ApiKey", p.Key.ID, action, t)
		if e != nil {
			return d, e
		}
	}
	d.Allowed = d.UserAllowed && d.KeyAllowed
	if d.Allowed {
		d.Reason = "ALLOWED"
	}
	return d, nil
}
func resolve(st *State, kind, id, env string) (Target, error) {
	if env == "" {
		env = "global"
	}
	t := Target{Kind: kind, ID: id, Environment: env}
	switch kind {
	case "system":
		if id != "system" {
			return t, notFound()
		}
	case "organization":
		if _, ok := st.Organizations[id]; !ok {
			return t, notFound()
		}
		t.Organization = id
	case "service", "model":
		r, ok := st.Resources[id]
		if !ok || r.Kind != kind {
			return t, notFound()
		}
		t.Organization = r.Organization
	case "api_key":
		k, ok := st.Keys[id]
		if !ok {
			return t, notFound()
		}
		t.Organization = k.Organization
		t.Owner = k.Owner
	default:
		return t, notFound()
	}
	return t, nil
}
func validatePolicy(st *State, p Principal, org, scope string, policy Policy) error {
	if policy.Version != 1 || len(policy.Statements) == 0 || len(policy.Statements) > 32 {
		return problem(422, "POLICY_INVALID", "schema_version 1 and 1–32 statements required")
	}
	for _, s := range policy.Statements {
		r := s.Resource
		if s.Effect != "allow" && s.Effect != "deny" || len(s.Actions) == 0 || len(s.Actions) > 64 {
			return problem(422, "POLICY_INVALID", "invalid effect or actions")
		}
		if r.AllSystem {
			if scope != "system" || len(r.IDs) > 0 || r.All || r.Organization != "" || !contains([]string{"*", "system", "organization", "service", "model", "api_key"}, r.Kind) {
				return problem(422, "POLICY_INVALID", "cross-workspace wildcard requires a system-scoped key and no organization or IDs")
			}
		} else {
			if scope == "organization" && r.Organization != org {
				return problem(422, "POLICY_INVALID", "selector must belong to this organization")
			}
			if r.All == (len(r.IDs) > 0) || !contains([]string{"*", "organization", "service", "model", "api_key"}, r.Kind) || (r.Kind == "*" && !r.All) {
				return problem(422, "POLICY_INVALID", "choose exact IDs or organization wildcard")
			}
			if r.All && (r.Organization == "" || !sourceAllowed(st, p.User, r.Organization)) {
				return problem(422, "POLICY_INVALID", "wildcard requires an accessible organization")
			}
		}
		envs := s.Conditions.Environments
		if len(envs) == 0 {
			envs = environments
		}
		for _, env := range envs {
			if !environmentValid(env) {
				return problem(422, "POLICY_INVALID", "unknown environment")
			}
		}
		for _, action := range s.Actions {
			if action != "*" && (len(actions[action]) == 0 || (r.Kind != "*" && !(r.AllSystem && r.Kind == "system") && !contains(actions[action], r.Kind))) {
				return problem(422, "POLICY_INVALID", "unknown action or incompatible resource kind")
			}
		}
		// Policies are ceilings, including exact selectors. The owner's current
		// permissions are evaluated by Authorize on every request.
		for _, id := range r.IDs {
			t, e := resolve(st, r.Kind, id, "global")
			if e != nil || t.Organization != r.Organization {
				return problem(422, "POLICY_INVALID", "invalid resource ownership")
			}
		}
	}
	_, err := compileKey(APIKey{ID: "validation", Policy: policy})
	return err
}
