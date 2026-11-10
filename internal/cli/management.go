package cli

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type managementRoute struct {
	method string
	path   string
}

var managementResources = map[string]struct {
	operations map[string]managementRoute
	usage      string
}{
	"org": {
		operations: map[string]managementRoute{
			"list":   {http.MethodGet, "/api/v1/orgs"},
			"create": {http.MethodPost, "/api/v1/orgs"},
		},
		usage: "use org list or create",
	},
	"members": {
		operations: map[string]managementRoute{
			"list":   {http.MethodGet, "{scope}/members"},
			"add":    {http.MethodPost, "{scope}/members"},
			"remove": {http.MethodDelete, "{scope}/members/{user}"},
		},
		usage: "use members list, add or remove",
	},
	"groups": {
		operations: map[string]managementRoute{
			"list":          {http.MethodGet, "{scope}/groups"},
			"create":        {http.MethodPost, "{scope}/groups"},
			"update":        {http.MethodPatch, "{scope}/groups/{id}"},
			"delete":        {http.MethodDelete, "{scope}/groups/{id}"},
			"add-member":    {http.MethodPut, "{scope}/groups/{group}/members/{user}"},
			"remove-member": {http.MethodDelete, "{scope}/groups/{group}/members/{user}"},
		},
		usage: "unknown groups operation",
	},
	"roles": {
		operations: map[string]managementRoute{
			"list":   {http.MethodGet, "{scope}/role-assignments"},
			"grant":  {http.MethodPost, "{scope}/role-assignments"},
			"update": {http.MethodPut, "{scope}/role-assignments/{id}"},
			"revoke": {http.MethodDelete, "{scope}/role-assignments/{id}"},
		},
		usage: "unknown roles operation",
	},
	"keys": {
		operations: map[string]managementRoute{
			"list":   {http.MethodGet, "{scope}/api-keys"},
			"create": {http.MethodPost, "{scope}/api-keys"},
			"update": {http.MethodPatch, "/api/v1/api-keys/{id}"},
			"show":   {http.MethodGet, "/api/v1/api-keys/{id}"},
			"revoke": {http.MethodDelete, "/api/v1/api-keys/{id}"},
		},
		usage: "unknown keys operation",
	},
	"models": {
		operations: map[string]managementRoute{
			"list":   {http.MethodGet, "{scope}/models"},
			"create": {http.MethodPost, "{scope}/models"},
			"get":    {http.MethodGet, "{scope}/models/{id}"},
			"update": {http.MethodPatch, "{scope}/models/{id}"},
			"delete": {http.MethodDelete, "{scope}/models/{id}"},
		},
		usage: "unknown resource operation",
	},
}

func (c *Client) manage(ctx context.Context, command string, args []string) error {
	if len(args) == 0 {
		return errors.New("subcommand required")
	}
	operation := args[0]
	f := flags(command, c.Err)
	org := c.orgFlag(f)
	system := f.Bool("system", false, "System scope")
	id := f.String("id", "", "Resource / key / assignment ID")
	name := f.String("name", "", "Name")
	shortName := f.String("short-name", "", "Organization short name")
	locator := f.String("locator", "", "Resource locator")
	user := f.String("user", "", "Local user ID")
	group := f.String("group", "", "Group ID")
	role := f.String("role", "", "Role ID")
	kind := f.String("scope-kind", "organization", "Role scope kind")
	scopeID := f.String("scope-id", "", "Role scope ID")
	subjectKind := f.String("subject-kind", "user", "Role subject kind")
	subject := f.String("subject", "", "Subject ID")
	envs := f.String("environments", "", "Comma-separated environments")
	file := f.String("file", "", "YAML input file")
	policy := f.String("policy", "", "Key policy YAML file")
	expires := f.String("expires", "24h", "Key lifetime, e.g. 24h, 0 or never")
	revision := f.Int("revision", 0, "Expected revision")
	if e := f.Parse(args[1:]); e != nil {
		return e
	}
	if f.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if command == "models" && strings.Contains(*id, "/") {
		if *system {
			return errors.New("invalid resource path")
		}
		if err := resolveResourcePath(f, command, org, id); err != nil {
			return err
		}
	}
	base := "/api/v1/orgs/" + url.PathEscape(*org)
	if *system {
		if flagSet(f, "org") {
			return errors.New("--system and --org cannot be combined")
		}
		base = "/api/v1/system"
	}
	resource := managementResources[command]
	route, ok := resource.operations[operation]
	if *org == "" && !*system && command != "org" && (!ok || strings.HasPrefix(route.path, "{scope}")) {
		return errors.New("specify --org, set a default with krun config set org, or use --system for system scope")
	}
	if !ok {
		return errors.New(resource.usage)
	}
	path := strings.NewReplacer(
		"{scope}", base,
		"{id}", url.PathEscape(*id),
		"{user}", url.PathEscape(*user),
		"{group}", url.PathEscape(*group),
	).Replace(route.path)

	var input any
	switch command + " " + operation {
	case "org create":
		values := map[string]string{"name": *name}
		if *shortName != "" {
			values["short_name"] = *shortName
		}
		input = values
	case "members add":
		input = map[string]string{"user_id": *user}
	case "groups create":
		input = map[string]string{"name": *name}
	case "groups update":
		if err := requireID(*id, "id"); err != nil {
			return err
		}
		if *file == "" && (strings.TrimSpace(*name) == "" || !flagSet(f, "revision") || *revision < 0) {
			return errors.New("--name and a non-negative --revision are required")
		}
		if *file != "" && (flagSet(f, "name") || flagSet(f, "revision")) {
			return errors.New("choose either --file or --name and --revision")
		}
		input = map[string]any{"name": *name, "expected_revision": *revision}
	case "roles grant", "roles update":
		if *system {
			*kind = "system"
			*scopeID = "system"
		} else if *scopeID == "" {
			*scopeID = *org
		}
		environments := []string{}
		if *envs != "" {
			environments = strings.Split(*envs, ",")
		}
		values := map[string]any{
			"subject_kind": *subjectKind,
			"subject_id":   *subject,
			"role_id":      *role,
			"scope_kind":   *kind,
			"scope_id":     *scopeID,
			"conditions":   map[string]any{"environments": environments},
		}
		if operation == "update" {
			values["expected_revision"] = *revision
		}
		input = values
	case "keys create", "keys update":
		p, err := dataFile(*policy)
		if err != nil {
			return err
		}
		if p == nil {
			return errors.New("--policy required")
		}
		if operation == "update" {
			input = map[string]any{"name": *name, "policy": p, "expected_revision": *revision}
			break
		}
		var lifetime time.Duration
		if *expires != "never" && *expires != "0" {
			lifetime, err = time.ParseDuration(*expires)
			if err != nil || lifetime < 60*time.Second || lifetime > 90*24*time.Hour {
				return errors.New("--expires must be never, 0 or a duration between 60s and 2160h")
			}
		}
		input = map[string]any{"name": *name, "expires_in": int(lifetime.Seconds()), "policy": p}
	case "models create":
		values := map[string]string{"name": *name}
		if *locator != "" {
			values["locator"] = *locator
		}
		input = values
	case "models update":
		input = map[string]any{"name": *name, "expected_revision": *revision}
	}
	if *file != "" {
		var err error
		input, err = dataFile(*file)
		if err != nil {
			return err
		}
	}
	return c.call(ctx, route.method, path, input)
}
