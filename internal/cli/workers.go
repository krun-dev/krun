package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

const workersHelp = `Usage: krun workers COMMAND [--org ID]
  list, create, get, update, delete   Manage Worker resources
  environments                      Manage production and preview environments
  previews-base                     Configure defaults for new previews
  configurations                    Save and deploy environment configuration snapshots
  domains                           Inspect and update routing domains
  network                           List and select network profiles
  repository                        Manage Git repository access and bindings
  invoke                            Send a test request to a deployed Worker
`

func (c *Client) workers(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "--help" {
		_, err := fmt.Fprint(c.Out, workersHelp)
		return err
	}
	action, rest := args[0], args[1:]
	switch action {
	case "list", "create", "get", "update", "delete":
		return c.workerResource(ctx, action, rest)
	case "environments", "previews-base":
		return c.environments(ctx, action, rest)
	case "configurations":
		return c.configurations(ctx, rest)
	case "domains", "network":
		return c.routing(ctx, action, rest)
	case "repository":
		return c.repository(ctx, rest)
	case "invoke":
		return c.invoke(ctx, rest)
	default:
		return fmt.Errorf("unknown workers command %q; use krun workers --help", action)
	}
}

func (c *Client) workerResource(ctx context.Context, action string, args []string) error {
	f := flags("workers "+action, c.Err)
	org := c.orgFlag(f)
	var id, name, locator, after, search string
	var revision, limit int
	switch action {
	case "list":
		f.IntVar(&limit, "limit", 100, "Page size (1–200)")
		f.StringVar(&after, "after", "", "Pagination token from next_after")
		f.StringVar(&search, "search", "", "Search by Worker name or ID")
	case "create":
		f.StringVar(&name, "name", "", "Worker name")
		f.StringVar(&locator, "locator", "", "Resource locator")
	default:
		f.StringVar(&id, "id", "", "Worker ID, locator or full resource path")
		if action == "update" {
			f.StringVar(&name, "name", "", "New Worker name")
			f.IntVar(&revision, "revision", -1, "Expected Worker revision")
		}
	}
	if err := parseFlags(f, args); err != nil {
		return err
	}
	if err := resolveResourcePath(f, "workers", org, &id); err != nil {
		return err
	}
	orgPath, err := organizationPath(*org)
	if err != nil {
		return err
	}
	base := orgPath + "/workers"
	switch action {
	case "list":
		if limit < 1 || limit > 200 {
			return errors.New("--limit must be between 1 and 200")
		}
		query := url.Values{"limit": {fmt.Sprint(limit)}}
		if after != "" {
			query.Set("after", after)
		}
		if search != "" {
			query.Set("q", search)
		}
		return c.call(ctx, "GET", base+"?"+query.Encode(), nil)
	case "create":
		if err := requireID(name, "name"); err != nil {
			return err
		}
		input := map[string]string{"name": name}
		if locator != "" {
			input["locator"] = locator
		}
		return c.call(ctx, "POST", base, input)
	}
	if err := requireID(id, "id"); err != nil {
		return err
	}
	path := base + "/" + url.PathEscape(id)
	switch action {
	case "update":
		if name == "" || revision < 0 {
			return errors.New("--name and a non-negative --revision are required")
		}
		return c.call(ctx, "PATCH", path, map[string]any{"name": name, "expected_revision": revision})
	case "delete":
		return c.call(ctx, "DELETE", path, nil)
	default:
		return c.call(ctx, "GET", path, nil)
	}
}

func (c *Client) routing(ctx context.Context, group string, args []string) error {
	if len(args) == 0 || args[0] == "--help" {
		if group == "network" {
			_, err := fmt.Fprintln(c.Out, "Usage: krun workers network list|set [options]")
			return err
		}
		_, err := fmt.Fprintln(c.Out, "Usage: krun workers domains list|get|set [options]")
		return err
	}
	action, args := args[0], args[1:]
	f := flags("workers "+group+" "+action, c.Err)
	if action == "list" {
		org := c.orgFlag(f)
		if err := parseFlags(f, args); err != nil {
			return err
		}
		base, err := organizationPath(*org)
		if err != nil {
			return err
		}
		path := "/workers/domains"
		if group == "network" {
			path = "/workers/network-profiles"
		}
		return c.call(ctx, "GET", base+path, nil)
	}

	target := c.workerFlags(f)
	var selected *string
	var revision *int
	if action == "set" {
		revision = revisionFlag(f, "Expected routing revision")
		if group == "network" {
			selected = f.String("network-profile", "", "Network profile ID")
		} else {
			selected = f.String("domains", "", "Comma-separated domain IDs (replaces the selection)")
		}
	} else if group != "domains" || action != "get" {
		return fmt.Errorf("unknown %s command %q", group, action)
	}
	base, err := target.path(f, args)
	if err != nil {
		return err
	}
	path := base + "/domains"
	if action == "get" {
		return c.call(ctx, "GET", path, nil)
	}
	if *selected == "" {
		return errors.New("select a network profile or at least one domain")
	}
	expected := *revision
	if expected < 0 {
		var current struct {
			Revision int `json:"revision"`
		}
		if err := c.request(ctx, "GET", path, nil, &current, false); err != nil {
			return err
		}
		expected = current.Revision
	}
	input := map[string]any{"expected_revision": expected}
	if group == "network" {
		path = base + "/network-profile"
		input["network_profile"] = *selected
	} else {
		input["domains"] = strings.Split(*selected, ",")
	}
	return c.call(ctx, "PUT", path, input)
}

func (c *Client) invoke(ctx context.Context, args []string) error {
	f := flags("workers invoke", c.Err)
	target := c.workerFlags(f)
	env := environmentFlag(f)
	path := f.String("path", "/", "Request path and optional query")
	method := f.String("method", http.MethodGet, "HTTP method")
	file := f.String("file", "", "File to send as the request body")
	base, err := target.path(f, args)
	if err != nil {
		return err
	}
	scope, err := environmentPath(base, *env)
	if err != nil {
		return err
	}
	input := map[string]string{"path": *path, "method": *method}
	if *file != "" {
		data, err := readInputFile(*file, 64<<10)
		if err != nil {
			return err
		}
		input["body"] = string(data)
	}
	var raw json.RawMessage
	if err := c.request(ctx, "POST", scope+"/invoke", input, &raw, false); err != nil {
		return err
	}
	var response struct {
		Status int `json:"status"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return err
	}
	if err := c.outputJSON(raw); err != nil {
		return err
	}
	if response.Status >= 400 {
		return fmt.Errorf("Worker returned HTTP %d", response.Status)
	}
	return nil
}
