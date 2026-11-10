package authorization

import (
	"regexp"
	"slices"
)

var actions = map[string][]string{
	"storage.bucket.create": {"organization"}, "storage.bucket.update": {"organization"},
	"storage.object.read": {"organization"}, "storage.object.write": {"organization"}, "storage.object.delete": {"organization"},
	"system.inventory.read": {"system"}, "system.config.read": {"system"}, "organization.create": {"system"}, "system.configure": {"system"}, "system.role.read": {"system"}, "system.role.grant": {"system"}, "system.role.revoke": {"system"}, "system.user.read": {"system"}, "system.user.update": {"system"}, "audit.read": {"system", "organization"},
	"organization.read": {"organization"}, "organization.update": {"organization"}, "organization.delete": {"organization"}, "member.read": {"organization"}, "member.manage": {"organization"}, "iam.read": {"organization"}, "iam.grant": {"organization"}, "iam.revoke": {"organization"},
	"apiKey.create": {"organization", "system"}, "apiKey.read": {"api_key"}, "apiKey.update": {"api_key"}, "apiKey.revoke": {"api_key"},
	"worker.list": {"organization"}, "worker.create": {"organization"}, "worker.read": {"worker"}, "worker.update": {"worker"}, "worker.delete": {"worker"}, "worker.publish": {"worker"}, "worker.rollback": {"worker"}, "worker.version.read": {"worker"}, "worker.version.create": {"worker"}, "worker.invoke": {"worker"},
	// Model CRUD covers resource catalog metadata. Lifecycle and invocation actions are reserved.
	"model.list": {"organization"}, "model.create": {"organization"}, "model.read": {"model"}, "model.update": {"model"}, "model.delete": {"model"}, "model.publish": {"model"}, "model.rollback": {"model"}, "model.version.read": {"model"}, "model.version.create": {"model"}, "model.use": {"model"}, "model.invoke": {"model"}, "build.start": {"worker", "model"}, "log.read": {"worker", "model"}, "source.read": {"worker", "model"}, "binding.attach": {"worker"}, "binding.detach": {"worker"},
}
var roleActions = map[string][]string{
	"viewer": {
		"organization.read",
		"worker.list", "worker.read", "worker.version.read",
		"model.list", "model.read", "model.version.read",
		"log.read", "source.read", "storage.object.read",
	},
	"member": {
		"organization.read",
		"worker.list", "worker.read", "worker.create", "worker.update", "worker.delete",
		"worker.version.create", "worker.version.read", "worker.publish", "worker.rollback", "worker.invoke",
		"model.list", "model.read", "model.create", "model.update", "model.delete",
		"model.version.create", "model.version.read", "model.publish", "model.rollback", "model.use", "model.invoke",
		"build.start", "log.read", "source.read", "binding.attach", "binding.detach",
		"storage.bucket.create", "storage.bucket.update",
		"storage.object.read", "storage.object.write", "storage.object.delete",
	},
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
	return []string{"system_admin", "org_admin", "member", "viewer"}, registered, slices.Clone(environments)
}
func OrganizationRoleValid(role string) bool   { return role == "org_admin" || roleActions[role] != nil }
func ReservedEnvironmentValid(env string) bool { return slices.Contains(environments, env) }
