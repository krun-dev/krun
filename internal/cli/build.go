package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

const buildHelp = `Usage: krun build COMMAND [--org ID] --worker ID
  start   Start a build from the configured repository, a source file or an uploaded bundle
  list    List builds
  get     Inspect a build
  wait    Wait for a build to succeed
  logs    Read build logs
  upload  Upload a src/ + assets/ tar.gz bundle for a build
`

func (c *Client) build(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "--help" {
		_, err := fmt.Fprint(c.Out, buildHelp)
		return err
	}
	action, args := args[0], args[1:]
	switch action {
	case "start":
		return c.buildStart(ctx, args)
	case "upload":
		return c.buildUpload(ctx, args)
	case "logs":
		return c.buildLogs(ctx, args)
	case "list", "get", "wait":
		f := flags("build "+action, c.Err)
		target := c.workerFlags(f)
		var id string
		var timeout time.Duration
		if action != "list" {
			f.StringVar(&id, "build", "", "Build ID")
		}
		if action == "wait" {
			f.DurationVar(&timeout, "timeout", 15*time.Minute, "Maximum wait time")
		}
		base, err := target.path(f, args)
		if err != nil {
			return err
		}
		path := base + "/builds"
		if action == "list" {
			return c.call(ctx, "GET", path, nil)
		}
		if err := requireID(id, "build"); err != nil {
			return err
		}
		path += "/" + url.PathEscape(id)
		if action == "wait" {
			return c.waitFor(ctx, path, id, timeout, buildTerminal)
		}
		return c.call(ctx, "GET", path, nil)
	default:
		return fmt.Errorf("unknown build command %q; use krun build --help", action)
	}
}

func (c *Client) buildStart(ctx context.Context, args []string) error {
	f := flags("build start", c.Err)
	target := c.workerFlags(f)
	env := environmentFlag(f)
	file := f.String("file", "", "Single JavaScript or TypeScript source file")
	upload := f.String("source-upload", "", "Bundle ID from build upload")
	entry := f.String("entrypoint", "", "Entry point in the uploaded bundle, including src/")
	assets := f.String("assets-config", "", "JSON or YAML static asset settings for an uploaded bundle")
	ref := f.String("ref", "", "Git ref (defaults to the bound repository ref)")
	repositoryRevision := f.Int("repository-revision", -1, "Expected repository revision (default: read it)")
	date := f.String("compatibility-date", "", "Worker runtime compatibility date")
	compatibilityFlags := f.String("compatibility-flags", "", "Comma-separated runtime compatibility flags")
	key := idempotencyFlag(f)
	base, err := target.path(f, args)
	if err != nil {
		return err
	}
	if err := requireID(*env, "env"); err != nil {
		return err
	}
	if *file != "" && *upload != "" {
		return errors.New("choose either --file or --source-upload")
	}
	local := *file != "" || *upload != ""
	if local && (*ref != "" || *repositoryRevision != -1) {
		return errors.New("--ref and --repository-revision require a repository build")
	}
	if *upload == "" && (*assets != "" || *entry != "") {
		return errors.New("--assets-config and --entrypoint require --source-upload")
	}

	input := map[string]any{"environment": *env}
	if *date != "" {
		input["compatibility_date"] = *date
	}
	if *compatibilityFlags != "" {
		input["compatibility_flags"] = strings.Split(*compatibilityFlags, ",")
	}
	switch {
	case *file != "":
		source, err := readInputFile(*file, 2<<20)
		if err != nil {
			return err
		}
		name := filepath.Base(*file)
		input["entrypoint"] = name
		input["files"] = map[string]string{name: string(source)}
	case *upload != "":
		if err := requireID(*entry, "entrypoint"); err != nil {
			return err
		}
		input["source_upload"] = *upload
		input["entrypoint"] = *entry
		if *assets != "" {
			var config map[string]any
			if err := decodeInputFile(*assets, &config); err != nil {
				return err
			}
			input["assets"] = config
		}
	default:
		if err := c.repositoryBuildInput(ctx, base, *ref, *repositoryRevision, input); err != nil {
			return err
		}
	}
	input["idempotency_key"] = c.submissionKey(*key)
	return c.call(ctx, "POST", base+"/builds", input)
}

func buildTerminal(state string) (bool, bool) {
	return state == "succeeded" || state == "failed", state == "succeeded"
}

func (c *Client) repositoryBuildInput(ctx context.Context, base, ref string, revision int, input map[string]any) error {
	if revision < 0 {
		binding, err := c.repositoryBinding(ctx, base)
		if err != nil {
			return err
		}
		if binding.Source == nil {
			return errors.New("no repository is bound; use krun workers repository set or supply a build from another source")
		}
		revision = binding.Revision
	}
	fmt.Fprintln(c.Err, "Repository revision:", revision)
	input["source_type"] = "repository"
	input["ref"] = ref
	input["repository_revision"] = revision
	return nil
}

func (c *Client) buildUpload(ctx context.Context, args []string) error {
	f := flags("build upload", c.Err)
	target := c.workerFlags(f)
	bundle := f.String("bundle", "", "Path to a portable tar.gz source bundle")
	base, err := target.path(f, args)
	if err != nil {
		return err
	}
	if err := requireID(*bundle, "bundle"); err != nil {
		return err
	}
	file, size, err := openRegularFile(*bundle)
	if err != nil {
		return err
	}
	defer file.Close()
	if size == 0 || size > 32<<20 {
		return errors.New("source bundle size must be between 1 byte and 32 MiB")
	}
	var grant struct {
		Key string `json:"source_upload"`
		URL string `json:"upload_url"`
	}
	if err := c.request(ctx, "POST", base+"/source-uploads", nil, &grant, false); err != nil {
		return err
	}
	if grant.Key == "" {
		return errors.New("missing source upload ID")
	}
	resp, err := transfer(ctx, http.MethodPut, grant.URL, nil, file, size)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return c.outputJSON(map[string]string{"source_upload": grant.Key})
}

func readInputFile(path string, limit int64) ([]byte, error) {
	file, _, err := openRegularFile(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return readLimited(file, limit)
}

func readLimited(source io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(source, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("input exceeds %d bytes", limit)
	}
	return data, nil
}
