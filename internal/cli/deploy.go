package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"time"
)

const deploymentHelp = `Usage: krun deployments COMMAND [--org ID] --worker ID
  list    List deployments
  status  Show the environment's traffic percentages and confirmed version
  view    Inspect a deployment by deployment ID
  wait    Wait for a deployment by operation ID
`

const versionHelp = `Usage: krun versions COMMAND [--org ID] --worker ID
  list    List versions
  view    Inspect a version
  upload  Build the bound repository and upload a version without changing traffic
  deploy  Deploy one or two versions using VERSION_ID[@PERCENTAGE%]
`

func (c *Client) deploy(ctx context.Context, action string, args []string, loadSession func() error) error {
	f := flags(action, c.Err)
	target := c.workerFlags(f)
	env := environmentFlag(f)
	revision := revisionFlag(f, "Expected deployment revision")
	key := idempotencyFlag(f)
	wait := f.Bool("wait", true, "Wait for runtime confirmation")
	timeout := f.Duration("timeout", 10*time.Minute, "Maximum wait time")
	var build releaseBuild
	var version string
	var versions []versionPercentage
	var message string
	var dryRun bool
	var outdir string
	if action != "deploy" {
		f.StringVar(&message, "message", "", "Description of this deployment")
		f.Bool("yes", false, "Accept defaults (the krun CLI does not prompt)")
		f.Bool("y", false, "Alias for --yes")
	}
	if action == "deploy" {
		build.flags(f)
		f.StringVar(&build.localPath, "path", "", "Upload a local directory or ZIP for deployment")
		f.StringVar(&build.projectRoot, "project-root", "", "Select the project root relative to --path")
		f.BoolVar(&dryRun, "dry-run", false, "Bundle the local checkout with Wrangler without deploying")
		f.StringVar(&outdir, "outdir", "", "Write the local bundle to this directory (requires --dry-run)")
		f.Usage = func() {
			fmt.Fprintln(c.Err, "Usage: krun deploy [options]")
			fmt.Fprintln(c.Err, "       krun deploy --dry-run [--config WRANGLER_FILE] [--outdir DIR]")
			f.PrintDefaults()
		}
	} else if action == "versions deploy" {
		f.Usage = func() {
			fmt.Fprintln(c.Err, "Usage: krun versions deploy VERSION_ID[@PERCENTAGE%] [VERSION_ID[@PERCENTAGE%]] [options]")
			fmt.Fprintln(c.Err, "Omitted percentages share the remaining traffic.")
			f.PrintDefaults()
		}
		specs, err := versionDeployArgs(f, args)
		if err != nil {
			return err
		}
		versions, err = versionDeployPercentages(specs)
		if err != nil {
			return err
		}
	} else {
		version, args = operand(args)
		f.Usage = func() {
			fmt.Fprintf(c.Err, "Usage: krun %s VERSION_ID [options]\n", action)
			f.PrintDefaults()
		}
	}
	if action != "versions deploy" {
		if err := parseFlags(f, args); err != nil {
			return err
		}
	}
	if dryRun {
		return c.deployDryRun(ctx, f, outdir)
	}
	if flagSet(f, "outdir") || c.configPath != "" {
		return errors.New("--config and --outdir require --dry-run; use --profile to select CLI credentials")
	}
	if action == "deploy" {
		if err := build.validate(f); err != nil {
			return err
		}
	} else if action == "rollback" {
		if err := requireOperand(version, "VERSION_ID"); err != nil {
			return err
		}
	}
	if *timeout <= 0 || (!*wait && flagSet(f, "timeout")) {
		return errors.New("--timeout must be positive and requires waiting")
	}
	if flagSet(f, "revision") && *revision < 0 {
		return errors.New("--revision must be non-negative")
	}
	if err := validateReleaseKey(*key); err != nil {
		return err
	}
	if loadSession != nil {
		if err := loadSession(); err != nil {
			return err
		}
		if !flagSet(f, "org") {
			*target.org = c.Profile.Organization
		}
	}
	base, err := target.resolvedPath(f)
	if err != nil {
		return err
	}
	scope, err := environmentPath(base, *env)
	if err != nil {
		return err
	}
	current, err := c.revision(ctx, base, *env, *revision, false)
	if err != nil {
		return err
	}
	fmt.Fprintln(c.Err, "Deployment revision:", current)
	releaseKey := c.submissionKey(*key)
	input := map[string]any{"expected_revision": current, "idempotency_key": releaseKey}
	switch {
	case action == "deploy":
		buildID, err := build.resolve(ctx, c, action, base, *env, releaseKey)
		if err != nil {
			return err
		}
		input["build_id"] = buildID
	case len(versions) == 2:
		input["versions"] = versions
	case len(versions) == 1:
		input["version_id"] = versions[0].ID
	default:
		input["version_id"] = version
	}
	if message != "" {
		input["message"] = message
	}
	path := scope + "/deployments"
	if action == "rollback" {
		path = scope + "/rollbacks"
	}
	if !*wait {
		return c.call(ctx, "POST", path, input)
	}
	var raw json.RawMessage
	if err := c.request(ctx, "POST", path, input, &raw, false); err != nil {
		return err
	}
	var result struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return err
	}
	if result.ID == "" {
		return errors.New("deployment response did not include an operation ID")
	}
	fmt.Fprintln(c.Err, "Operation ID:", result.ID)
	return c.waitForDeployment(ctx, base, result.ID, *timeout)
}

func (c *Client) deployments(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "--help" {
		_, err := fmt.Fprint(c.Out, deploymentHelp)
		return err
	}
	action, args := args[0], args[1:]
	if action == "list" || action == "view" {
		return c.releaseRead(ctx, "deployments", action, args)
	}
	f := flags("deployments "+action, c.Err)
	target := c.workerFlags(f)
	switch action {
	case "status":
		env := environmentFlag(f)
		base, err := target.path(f, args)
		if err != nil {
			return err
		}
		if err := requireID(*env, "env"); err != nil {
			return err
		}
		current, err := c.environment(ctx, base, *env)
		if err != nil {
			return err
		}
		return c.outputJSON(map[string]any{
			"environment": current.Name, "version_id": current.Version,
			"confirmed_version_id": current.Confirmed, "deployment_revision": current.Revision,
			"versions": releasePercentages(current.Version, current.WorkerGradualRelease),
		})
	case "wait":
		id := f.String("operation", "", "Operation ID returned by deployment or rollback")
		timeout := f.Duration("timeout", 10*time.Minute, "Maximum wait time")
		base, err := target.path(f, args)
		if err != nil {
			return err
		}
		if err := requireID(*id, "operation"); err != nil {
			return err
		}
		return c.waitForDeployment(ctx, base, *id, *timeout)
	default:
		return fmt.Errorf("unknown deployments command %q; use krun deployments --help", action)
	}
}

func (c *Client) waitForDeployment(ctx context.Context, base, id string, timeout time.Duration) error {
	return c.waitFor(ctx, base+"/operations/"+url.PathEscape(id), id, timeout, func(state string) (bool, bool) {
		switch state {
		case "available", "succeeded":
			return true, true
		case "failed", "superseded", "cancelled":
			return true, false
		default:
			return false, false
		}
	})
}

func (c *Client) versions(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "--help" {
		_, err := fmt.Fprint(c.Out, versionHelp)
		return err
	}
	action, args := args[0], args[1:]
	switch action {
	case "deploy":
		return c.deploy(ctx, "versions deploy", args, nil)
	case "upload":
		return c.versionUpload(ctx, args)
	case "list", "view":
		return c.releaseRead(ctx, "versions", action, args)
	default:
		return fmt.Errorf("unknown versions command %q; use krun versions --help", action)
	}
}

func (c *Client) versionUpload(ctx context.Context, args []string) error {
	f := flags("versions upload", c.Err)
	target := c.workerFlags(f)
	env := environmentFlag(f)
	var build releaseBuild
	build.flags(f)
	tag := f.String("tag", "", "Version tag")
	message := f.String("message", "", "Version message")
	revision := f.Int("revision", -1, "Expected configuration revision (optional)")
	key := idempotencyFlag(f)
	base, err := target.path(f, args)
	if err != nil {
		return err
	}
	if err := build.validate(f); err != nil {
		return err
	}
	scope, err := environmentPath(base, *env)
	if err != nil {
		return err
	}
	if flagSet(f, "revision") && *revision < 0 {
		return errors.New("--revision must be non-negative")
	}
	if err := validateReleaseKey(*key); err != nil {
		return err
	}
	releaseKey := c.submissionKey(*key)
	buildID, err := build.resolve(ctx, c, "versions upload", base, *env, releaseKey)
	if err != nil {
		return err
	}
	input := map[string]any{
		"build_id": buildID, "tag": *tag, "message": *message,
		"idempotency_key": releaseKey,
	}
	if *revision >= 0 {
		input["expected_configuration_revision"] = *revision
	}
	return c.call(ctx, "POST", scope+"/versions", input)
}

func (c *Client) releaseRead(ctx context.Context, group, action string, args []string) error {
	f := flags(group+" "+action, c.Err)
	target := c.workerFlags(f)
	if action == "view" {
		id, args := operand(args)
		f.Usage = func() {
			fmt.Fprintf(c.Err, "Usage: krun %s view ID [options]\n", group)
			f.PrintDefaults()
		}
		base, err := target.path(f, args)
		if err != nil {
			return err
		}
		if err := requireOperand(id, "ID"); err != nil {
			return err
		}
		path := base + "/" + group + "/" + url.PathEscape(id)
		if group == "deployments" {
			deployment, err := c.readDeployment(ctx, path)
			if err != nil {
				return err
			}
			return c.outputJSON(deployment)
		}
		return c.call(ctx, "GET", path, nil)
	}
	env := f.String("env", "", "Filter by environment")
	after := f.String("after", "", "Pagination token from next_after")
	limit := f.Int("limit", 100, "Page size (1–200)")
	base, err := target.path(f, args)
	if err != nil {
		return err
	}
	if *limit < 1 || *limit > 200 {
		return errors.New("--limit must be between 1 and 200")
	}
	query := url.Values{"limit": {fmt.Sprint(*limit)}}
	if *env != "" {
		query.Set("environment", *env)
	}
	if *after != "" {
		query.Set("after", *after)
	}
	path := base + "/" + group + "?" + query.Encode()
	if group != "deployments" {
		return c.call(ctx, "GET", path, nil)
	}
	var page struct {
		Items []deploymentView `json:"items"`
		Next  string           `json:"next_after"`
	}
	if err := c.request(ctx, "GET", path, nil, &page, false); err != nil {
		return err
	}
	for i := range page.Items {
		page.Items[i].setPercentages()
	}
	return c.outputJSON(page)
}

func validateReleaseKey(key string) error {
	if len(key) > 128 {
		return errors.New("--idempotency-key must be at most 128 bytes")
	}
	return nil
}
