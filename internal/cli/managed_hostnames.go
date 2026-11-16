package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"

	"github.com/krun-dev/krun/internal/domain"
)

func (c *Client) managedHostnames(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "--help" {
		_, err := fmt.Fprint(c.Out, `Usage: krun domains COMMAND [--org ID]
  list [--worker ID]                               List domains, optionally for a Worker
  claim --suffix ID --label LABEL [--routes FILE]  Claim an application hostname
  set --host HOST --revision N --routes FILE       Replace the host's route table
  retire --host HOST --revision N                  Remove routing and reserve the hostname
`)
		return err
	}

	action := args[0]
	f := flags("domains "+action, c.Err)
	org := c.orgFlag(f)
	var worker, suffix, label, host, routeFile string
	var revision int
	switch action {
	case "list":
		f.StringVar(&worker, "worker", "", "Worker ID or locator")
	case "claim":
		f.StringVar(&suffix, "suffix", "", "Platform suffix ID")
		f.StringVar(&label, "label", "", "Single DNS label")
		f.StringVar(&routeFile, "routes", "", "JSON file containing an array of routes")
	case "set", "retire":
		f.StringVar(&host, "host", "", "Full hostname")
		f.IntVar(&revision, "revision", 0, "Current hostname revision")
		if action == "set" {
			f.StringVar(&routeFile, "routes", "", "JSON file containing an array of routes")
		}
	default:
		return fmt.Errorf("unknown domains command %q", action)
	}
	if err := parseFlags(f, args[1:]); err != nil {
		return err
	}
	base, err := organizationPath(*org)
	if err != nil {
		return err
	}
	path := base + "/domains"
	if action == "list" {
		if worker != "" {
			path = base + "/workers/" + url.PathEscape(worker) + "/hostnames"
		}
		return c.call(ctx, http.MethodGet, path, nil)
	}

	routes := []domain.ManagedHostnameRoute{}
	if routeFile != "" {
		data, err := os.ReadFile(routeFile)
		if err != nil {
			return err
		}
		if err = json.Unmarshal(data, &routes); err != nil || routes == nil {
			return errors.New("--routes must contain a JSON array of route objects")
		}
	}
	if action == "claim" {
		if suffix == "" || label == "" {
			return errors.New("--suffix and --label are required")
		}
		return c.call(ctx, http.MethodPost, path, map[string]any{"suffix_id": suffix, "label": label, "routes": routes})
	}
	if host == "" || revision < 1 {
		return errors.New("--host and a positive --revision are required")
	}
	path += "/" + url.PathEscape(host)
	if action == "retire" {
		return c.call(ctx, http.MethodDelete, path+"?revision="+fmt.Sprint(revision), nil)
	}
	if routeFile == "" {
		return errors.New("--routes is required")
	}
	return c.call(ctx, http.MethodPut, path, map[string]any{"expected_revision": revision, "routes": routes})
}

func (c *Client) workerCustomDomains(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "--help" {
		_, err := fmt.Fprint(c.Out, `Usage: krun workers custom-domains COMMAND --org ID --worker ID
  list                                     List Worker bindings and eligible custom suffixes
  attach --suffix ID --label LABEL          Bind an entire hostname to this Worker
  detach --host HOST --revision N           Remove a binding
`)
		return err
	}

	action := args[0]
	f := flags("workers custom-domains "+action, c.Err)
	target := c.workerFlags(f)
	var suffix, label, host string
	var revision int
	switch action {
	case "list":
	case "attach":
		f.StringVar(&suffix, "suffix", "", "Platform suffix ID")
		f.StringVar(&label, "label", "", "Single DNS label")
	case "detach":
		f.StringVar(&host, "host", "", "Full hostname")
		f.IntVar(&revision, "revision", 0, "Current hostname revision")
	default:
		return fmt.Errorf("unknown custom-domains command %q", action)
	}
	base, err := target.path(f, args[1:])
	if err != nil {
		return err
	}
	base += "/hostnames"
	switch action {
	case "attach":
		if suffix == "" || label == "" {
			return errors.New("--suffix and --label are required")
		}
		return c.call(ctx, http.MethodPost, base, map[string]string{"suffix_id": suffix, "label": label})
	case "detach":
		if host == "" || revision < 1 {
			return errors.New("--host and a positive --revision are required")
		}
		return c.call(ctx, http.MethodDelete, base+"/"+url.PathEscape(host)+"?revision="+fmt.Sprint(revision), nil)
	default:
		return c.call(ctx, http.MethodGet, base, nil)
	}
}
