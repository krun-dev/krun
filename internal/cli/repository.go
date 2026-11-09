package cli

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/krun-dev/krun/internal/domain"
)

const repositoryHelp = `Usage: krun workers repository COMMAND [--org ID]
  providers    List providers and your authorization status
  authorize    Open a Git provider authorization page
  list         List repositories available to your authorized account
  disconnect   Remove your authorization for a provider
  get          Show a Worker's repository binding
  set          Bind a repository to a Worker
  clear        Remove a Worker's repository binding
`

type repositoryBinding struct {
	Source   *domain.WorkerRepositorySource `json:"source"`
	Revision int                            `json:"revision"`
}

func (c *Client) repositoryBinding(ctx context.Context, base string) (repositoryBinding, error) {
	var result repositoryBinding
	err := c.request(ctx, "GET", base+"/repository", nil, &result, false)
	return result, err
}

func (c *Client) repository(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "--help" {
		_, err := fmt.Fprint(c.Out, repositoryHelp)
		return err
	}
	action, args := args[0], args[1:]
	f := flags("workers repository "+action, c.Err)
	switch action {
	case "providers", "authorize", "list", "disconnect":
		return c.repositoryAuthorization(ctx, action, args)
	case "get", "set", "clear":
		target := c.workerFlags(f)
		var provider, repository, ref, directory string
		var private bool
		var revision *int
		if action != "get" {
			revision = revisionFlag(f, "Expected repository revision")
		}
		if action == "set" {
			f.StringVar(&provider, "provider", "", "Repository provider ID")
			f.StringVar(&repository, "repository", "", "Provider-relative repository name, such as owner/project")
			f.StringVar(&ref, "ref", "HEAD", "Git ref")
			f.StringVar(&directory, "directory", ".", "Project directory inside the repository")
			f.BoolVar(&private, "private", false, "Use your Git repository authorization")
		}
		base, err := target.path(f, args)
		if err != nil {
			return err
		}
		if action == "get" {
			return c.call(ctx, "GET", base+"/repository", nil)
		}
		var source *domain.WorkerRepositorySource
		if action == "set" {
			if provider == "" || repository == "" || ref == "" || directory == "" {
				return errors.New("--provider, --repository, --ref and --directory must be nonempty")
			}
			source = &domain.WorkerRepositorySource{
				ProviderID: provider, Repository: repository, Ref: ref, RootDirectory: directory,
			}
		}
		expected := *revision
		if expected < 0 {
			current, err := c.repositoryBinding(ctx, base)
			if err != nil {
				return err
			}
			expected = current.Revision
		}
		return c.call(ctx, "PUT", base+"/repository", map[string]any{
			"source": source, "expected_revision": expected, "use_authorization": private,
		})
	default:
		return fmt.Errorf("unknown repository command %q; use krun workers repository --help", action)
	}
}

func (c *Client) repositoryAuthorization(ctx context.Context, action string, args []string) error {
	f := flags("workers repository "+action, c.Err)
	org := c.orgFlag(f)
	var provider string
	var browser bool
	var page int
	if action == "list" || action == "disconnect" || action == "authorize" {
		f.StringVar(&provider, "provider", "", "Repository provider ID")
	}
	if action == "authorize" {
		f.BoolVar(&browser, "browser", true, "Open the provider authorization in your browser")
	}
	if action == "list" {
		f.IntVar(&page, "page", 1, "Repository page (1–1000)")
	}
	if err := parseFlags(f, args); err != nil {
		return err
	}
	base, err := organizationPath(*org)
	if err != nil {
		return err
	}
	if action == "providers" {
		return c.call(ctx, "GET", base+"/workers/repository-providers", nil)
	}
	if err := requireID(provider, "provider"); err != nil {
		return err
	}
	path := base + "/worker-repositories/" + url.PathEscape(provider)
	if action == "authorize" {
		var launch struct {
			Path string `json:"browser_path"`
		}
		if err := c.request(ctx, "POST", path+"/authorize", nil, &launch, false); err != nil {
			return err
		}
		target, err := url.Parse(launch.Path)
		if err != nil || target.Host != "" || target.IsAbs() || !strings.HasPrefix(target.Path, "/api/v1/") ||
			!strings.HasSuffix(target.Path, "/authorize") || target.Query().Get("ticket") == "" {
			return errors.New("server returned an invalid repository authorization path")
		}
		address := strings.TrimRight(c.Profile.Endpoint, "/") + target.String()
		fmt.Fprintf(c.Err, "Open %s in a browser signed in to the same krun account. Confirm access at the Git provider, then run krun workers repository list --provider %s.\n", address, provider)
		if browser {
			openLoginBrowser(address)
		}
		return nil
	}
	if action == "disconnect" {
		return c.call(ctx, "DELETE", path+"/disconnect", nil)
	}
	if page < 1 || page > 1000 {
		return errors.New("--page must be between 1 and 1000")
	}
	return c.call(ctx, "POST", path+"/repositories", map[string]int{"page": page})
}
