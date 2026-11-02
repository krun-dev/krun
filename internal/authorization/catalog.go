package authorization

import (
	"regexp"
	"slices"
)

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
var previewSlug = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

var environments = []string{"global", "development", "production"}

func EnvironmentValid(v string) bool {
	return slices.Contains(environments, v) || (v != "test" && v != "staging" && previewSlug.MatchString(v))
}

// Catalog returns independent copies for API clients; callers cannot change the engine's registry.
func Catalog() (roles []string, registered map[string][]string, reservedEnvironments []string) {
	registered = make(map[string][]string, len(actions))
	for action, kinds := range actions {
		registered[action] = slices.Clone(kinds)
	}
	return []string{"system_admin", "org_admin", "developer", "publisher", "viewer"}, registered, slices.Clone(environments)
}
func OrganizationRoleValid(role string) bool   { return role == "org_admin" || roleActions[role] != nil }
func ReservedEnvironmentValid(env string) bool { return slices.Contains(environments, env) }
