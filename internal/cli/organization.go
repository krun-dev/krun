package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/krun-dev/krun/internal/domain"
)

const organizationHelp = `Usage: krun org COMMAND
  list                                  List accessible organizations
  create --name NAME [--short-name ORG]  Create an organization
  get [--org ORG]                       Show organization settings
  permissions [--org ORG]               Show your effective global organization permissions
  update [--org ORG] [--revision N]
         [--name NAME] [--allowed-source SOURCE ... | --clear-allowed-sources]
  invitations --help                    Manage organization invitation links

Organization commands use the profile default unless --org is specified.
`

const invitationHelp = `Usage: krun org invitations COMMAND [--org ORG]
  create [--role member|viewer] [--max-uses N] [--target-user ID | --target-email EMAIL]
  list [--limit N] [--cursor CURSOR]
  revoke --id ID

Use a CLI login session to manage invitations. The full link appears only on create.
`

func (c *Client) organization(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "--help" {
		_, err := fmt.Fprint(c.Out, organizationHelp)
		return err
	}
	action, rest := args[0], args[1:]
	switch action {
	case "list", "create":
		return c.manage(ctx, "org", args)
	case "get", "permissions":
		f := flags("org "+action, c.Err)
		org := c.orgFlag(f)
		if err := parseFlags(f, rest); err != nil {
			return err
		}
		path, err := organizationPath(*org)
		if err != nil {
			return err
		}
		if action == "permissions" {
			path += "/permissions"
		}
		return c.call(ctx, http.MethodGet, path, nil)
	case "update":
		return c.updateOrganization(ctx, rest)
	case "invitations":
		return c.organizationInvitations(ctx, rest)
	default:
		return fmt.Errorf("unknown org command %q; use krun org --help", action)
	}
}

func (c *Client) updateOrganization(ctx context.Context, args []string) error {
	f := flags("org update", c.Err)
	org := c.orgFlag(f)
	name := f.String("name", "", "New organization name")
	revision := revisionFlag(f, "Expected organization revision")
	clearSources := f.Bool("clear-allowed-sources", false, "Allow all enabled identity sources")
	sources := []string{}
	f.Func("allowed-source", "Replace the allowed identity sources; repeat for each source ID", func(value string) error {
		value = strings.TrimSpace(value)
		if value == "" {
			return errors.New("source ID is required")
		}
		sources = append(sources, value)
		return nil
	})
	if err := parseFlags(f, args); err != nil {
		return err
	}

	hasName := flagSet(f, "name")
	switch {
	case hasName && strings.TrimSpace(*name) == "":
		return errors.New("--name must not be empty")
	case *clearSources && len(sources) > 0:
		return errors.New("choose either --allowed-source or --clear-allowed-sources")
	case !hasName && len(sources) == 0 && !*clearSources:
		return errors.New("specify --name, --allowed-source, or --clear-allowed-sources")
	case flagSet(f, "revision") && *revision < 0:
		return errors.New("--revision must be non-negative")
	}

	path, err := organizationPath(*org)
	if err != nil {
		return err
	}
	var current domain.Organization
	if err := c.request(ctx, http.MethodGet, path, nil, &current, false); err != nil {
		return err
	}
	if !hasName {
		*name = current.Name
	}
	if !*clearSources && len(sources) == 0 {
		sources = current.Sources
	}
	if *revision < 0 {
		*revision = current.Revision
	}
	input := map[string]any{
		"name":              *name,
		"allowed_sources":   sources,
		"expected_revision": *revision,
	}
	return c.call(ctx, http.MethodPatch, path, input)
}

func (c *Client) organizationInvitations(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "--help" {
		_, err := fmt.Fprint(c.Out, invitationHelp)
		return err
	}
	action := args[0]
	f := flags("org invitations "+action, c.Err)
	org := c.orgFlag(f)
	var role, targetUser, targetEmail, cursor, id string
	var maxUses, limit int
	switch action {
	case "create":
		f.StringVar(&role, "role", "member", "Invitation role (member or viewer)")
		f.IntVar(&maxUses, "max-uses", 1, "Maximum number of new members (1–100)")
		f.StringVar(&targetUser, "target-user", "", "Invite only this local user ID")
		f.StringVar(&targetEmail, "target-email", "", "Invite only an account with this verified email")
	case "list":
		f.IntVar(&limit, "limit", 20, "Page size (1–100)")
		f.StringVar(&cursor, "cursor", "", "Pagination cursor from next_cursor")
	case "revoke":
		f.StringVar(&id, "id", "", "Invitation ID")
	default:
		return fmt.Errorf("unknown invitation command %q; use krun org invitations --help", action)
	}
	if err := parseFlags(f, args[1:]); err != nil {
		return err
	}
	path, err := organizationPath(*org)
	if err != nil {
		return err
	}
	path += "/invitation-links"

	switch action {
	case "create":
		hasTargetUser := flagSet(f, "target-user")
		hasTargetEmail := flagSet(f, "target-email")
		targetUser = strings.TrimSpace(targetUser)
		targetEmail = strings.TrimSpace(targetEmail)
		switch {
		case role != "member" && role != "viewer":
			return errors.New("--role must be member or viewer")
		case maxUses < 1 || maxUses > 100:
			return errors.New("--max-uses must be between 1 and 100")
		case hasTargetUser && hasTargetEmail:
			return errors.New("choose either --target-user or --target-email")
		case hasTargetUser && targetUser == "":
			return errors.New("--target-user must not be empty")
		case hasTargetEmail && targetEmail == "":
			return errors.New("--target-email must not be empty")
		}
		input := map[string]any{"role_id": role, "max_uses": maxUses}
		if hasTargetUser {
			input["target_user_id"] = targetUser
		}
		if hasTargetEmail {
			input["target_email"] = targetEmail
		}
		return c.call(ctx, http.MethodPost, path, input)
	case "list":
		if limit < 1 || limit > 100 {
			return errors.New("--limit must be between 1 and 100")
		}
		query := url.Values{"limit": {strconv.Itoa(limit)}}
		if cursor != "" {
			query.Set("cursor", cursor)
		}
		return c.call(ctx, http.MethodGet, path+"?"+query.Encode(), nil)
	default:
		if err := requireID(strings.TrimSpace(id), "id"); err != nil {
			return err
		}
		return c.call(ctx, http.MethodDelete, path+"/"+url.PathEscape(id), nil)
	}
}
