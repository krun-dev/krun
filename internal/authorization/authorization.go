package authorization

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	cedar "github.com/cedar-policy/cedar-go"
	"github.com/krun-dev/krun/internal/domain"
)

type Principal struct {
	OAuthScope   string
	OAuthActions []string
	User         domain.User
	Kind         string
	Key          *domain.APIKey
	SessionHash  string
	cache        *requestCache
}
type Target struct{ Kind, ID, Organization, Owner, Environment string }
type Decision struct {
	Allowed     bool   `json:"allowed"`
	UserAllowed bool   `json:"user_allowed"`
	KeyAllowed  bool   `json:"key_allowed"`
	Revision    int    `json:"revision"`
	Reason      string `json:"reason"`
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
func RoleMatches(st *domain.State, a domain.Assignment, u domain.User) bool {
	if a.SubjectKind == "user" {
		return a.SubjectID == u.ID
	}
	g, ok := st.Groups[a.SubjectID]
	return ok && ((g.ScopeKind == "system" && a.ScopeKind == "system") || (g.ScopeKind == "organization" && a.ScopeKind != "system" && g.ScopeID == ScopeOrganization(st, a))) && slices.Contains(g.Members, u.ID)
}
func ScopeOrganization(st *domain.State, a domain.Assignment) string {
	if a.ScopeKind == "organization" {
		return a.ScopeID
	}
	return st.Resources[a.ScopeID].Organization
}
func IsAdmin(st *domain.State, u domain.User, org string) bool {
	if u.Status != "active" {
		return false
	}
	for _, a := range st.Assignments {
		if !RoleMatches(st, a, u) {
			continue
		}
		if a.Role == "system_admin" && a.ScopeKind == "system" {
			return true
		}
		if org != "" && a.Role == "org_admin" && a.ScopeKind == "organization" && a.ScopeID == org {
			if _, ok := st.Members[domain.MemberKey(org, u.ID)]; ok {
				return true
			}
		}
	}
	return false
}
func SourceAllowed(st *domain.State, u domain.User, org string) bool {
	if org == "" {
		return true
	}
	o, ok := st.Organizations[org]
	return ok && (len(o.Sources) == 0 || slices.Contains(o.Sources, u.Source))
}
func addPolicy(ps *cedar.PolicySet, id, body string) error {
	var p cedar.Policy
	if e := p.UnmarshalCedar([]byte(body)); e != nil {
		return e
	}
	ps.Add(cedar.PolicyID(id), &p)
	return nil
}
func compileUser(st *domain.State, u domain.User) (*cedar.PolicySet, error) {
	ps := cedar.NewPolicySet()
	principal := "principal == User::" + strconv.Quote(u.ID)
	for _, a := range st.Assignments {
		if !RoleMatches(st, a, u) {
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
			org := ScopeOrganization(st, a)
			if _, ok := st.Members[domain.MemberKey(org, u.ID)]; !ok || !SourceAllowed(st, u, org) {
				continue
			}
			if a.ScopeKind == "organization" {
				resource = "resource in " + uid("organization", org)
			} else {
				resource = "resource == " + uid(a.ScopeKind, a.ScopeID)
			}
			if a.Role == "org_admin" {
				for action, kinds := range actions {
					if !strings.HasPrefix(action, "system.") && action != "organization.create" && !slices.Contains(kinds, "system") {
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
				actionResource = "resource == " + uid("organization", ScopeOrganization(st, a))
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
		if m.User != u.ID || !SourceAllowed(st, u, m.Organization) {
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
func statementActions(s domain.Statement) []string {
	set := map[string]bool{}
	for _, action := range s.Actions {
		if action != "*" {
			set[action] = true
			continue
		}
		for name, kinds := range actions {
			if s.Resource.Kind == "*" || slices.Contains(kinds, s.Resource.Kind) {
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

func compileKey(k domain.APIKey) (*cedar.PolicySet, error) {
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
		if !selector.AllSystem {
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

func Authorize(st *domain.State, p Principal, action string, t Target) (Decision, error) {
	cache := p.cache
	if cache != nil && (cache.state != st || cache.revision != st.Revision) {
		cache = nil
	}
	if p.OAuthActions != nil && !slices.Contains(p.OAuthActions, action) {
		return Decision{Revision: st.Revision, Reason: "OAUTH_SCOPE"}, nil
	}
	d := Decision{Revision: st.Revision, KeyAllowed: p.Key == nil, Reason: "PERMISSION_DENIED"}
	if !slices.Contains(actions[action], t.Kind) || !EnvironmentValid(t.Environment) || p.User.Status != "active" || !SourceAllowed(st, p.User, t.Organization) {
		return d, nil
	}
	var ps *cedar.PolicySet
	var e error
	if cache != nil {
		ps = cache.users[p.User.ID]
	}
	if ps == nil {
		ps, e = compileUser(st, p.User)
		if e == nil && cache != nil {
			cache.users[p.User.ID] = ps
		}
	}
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
		ps = nil
		if cache != nil {
			ps = cache.keys[p.Key.ID]
		}
		if ps == nil {
			ps, e = compileKey(*p.Key)
			if e == nil && cache != nil {
				cache.keys[p.Key.ID] = ps
			}
		}
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
func Resolve(st *domain.State, kind, id, env string) (Target, error) {
	if env == "" {
		env = "global"
	}
	t := Target{Kind: kind, ID: id, Environment: env}
	switch kind {
	case "system":
		if id != "system" {
			return t, domain.NotFound()
		}
	case "organization":
		if _, ok := st.Organizations[id]; !ok {
			return t, domain.NotFound()
		}
		t.Organization = id
	case "service", "model":
		r, ok := st.Resources[id]
		if !ok || r.Kind != kind {
			return t, domain.NotFound()
		}
		t.Organization = r.Organization
	case "api_key":
		k, ok := st.Keys[id]
		if !ok {
			return t, domain.NotFound()
		}
		t.Organization = k.Organization
		t.Owner = k.Owner
	default:
		return t, domain.NotFound()
	}
	return t, nil
}
