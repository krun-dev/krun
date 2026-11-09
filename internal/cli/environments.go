package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/krun-dev/krun/internal/configfile"
	"github.com/krun-dev/krun/internal/domain"
)

type bindingsFlags struct {
	variables *string
	buckets   *string
	metadata  *string
}

func workerBindingsFlags(f *flag.FlagSet) bindingsFlags {
	return bindingsFlags{
		variables: f.String("vars-file", "", "JSON or YAML variable map (replaces all variables)"),
		buckets:   f.String("r2-bindings-file", "", "JSON or YAML map of binding names to bucket names (replaces all R2 bindings)"),
		metadata:  f.String("version-metadata", "", "Version metadata binding name (empty removes it)"),
	}
}

func flagSet(f *flag.FlagSet, name string) bool {
	found := false
	f.Visit(func(option *flag.Flag) {
		if option.Name == name {
			found = true
		}
	})
	return found
}

func decodeInputFile(path string, output any) error {
	raw, err := readInputFile(path, 1<<20)
	if err != nil {
		return err
	}
	if strings.EqualFold(filepath.Ext(path), ".json") {
		return configfile.DecodeJSON(raw, output)
	}
	return configfile.Decode(path, raw, output)
}

func (b bindingsFlags) input(f *flag.FlagSet) (map[string]any, error) {
	input := map[string]any{}
	if *b.variables != "" {
		var value map[string]any
		if err := decodeInputFile(*b.variables, &value); err != nil {
			return nil, err
		}
		if value == nil {
			return nil, errors.New("--vars-file must contain an object")
		}
		input["variables"] = value
	}
	if *b.buckets != "" {
		var value map[string]string
		if err := decodeInputFile(*b.buckets, &value); err != nil {
			return nil, err
		}
		if value == nil {
			return nil, errors.New("--r2-bindings-file must contain an object")
		}
		input["r2_buckets"] = value
	}
	if flagSet(f, "version-metadata") {
		input["version_metadata"] = *b.metadata
	}
	return input, nil
}

func (c *Client) environments(ctx context.Context, group string, args []string) error {
	preview := group == "previews-base"
	if len(args) == 0 || args[0] == "--help" {
		if preview {
			_, err := fmt.Fprintln(c.Out, "Usage: krun workers previews-base get|update [options]")
			return err
		}
		_, err := fmt.Fprintln(c.Out, "Usage: krun workers environments list|get|create|update|delete [options]")
		return err
	}
	action, args := args[0], args[1:]
	if preview && action != "get" && action != "update" {
		return fmt.Errorf("unknown previews-base command %q", action)
	}
	f := flags("workers "+group+" "+action, c.Err)
	target := c.workerFlags(f)
	var env string
	if !preview && action != "list" {
		f.StringVar(&env, "env", "", "Environment name (required)")
	}
	var ignoreBase, enabled bool
	var revision *int
	var bindings bindingsFlags
	switch action {
	case "create":
		f.BoolVar(&ignoreBase, "ignore-base", false, "Start without preview defaults")
	case "update":
		revision = revisionFlag(f, "Expected configuration revision")
		bindings = workerBindingsFlags(f)
		if !preview {
			f.BoolVar(&enabled, "enabled", true, "Enable or disable the environment")
		}
	case "list", "get", "delete":
	default:
		return fmt.Errorf("unknown environments command %q", action)
	}
	base, err := target.path(f, args)
	if err != nil {
		return err
	}
	if action == "list" {
		return c.call(ctx, "GET", base+"/environments", nil)
	}
	path := base + "/previews-base"
	if !preview {
		path, err = environmentPath(base, env)
		if err != nil {
			return err
		}
	}
	switch action {
	case "get":
		if preview {
			return c.call(ctx, "GET", path, nil)
		}
		current, err := c.environment(ctx, base, env)
		if err != nil {
			return err
		}
		return c.outputJSON(current)
	case "create":
		return c.call(ctx, "POST", base+"/environments", map[string]any{"name": env, "ignore_base_config": ignoreBase})
	case "delete":
		return c.call(ctx, "DELETE", path, nil)
	}

	input, err := bindings.input(f)
	if err != nil {
		return err
	}
	if flagSet(f, "enabled") {
		input["enabled"] = enabled
	}
	if len(input) == 0 {
		return errors.New("specify at least one configuration change")
	}
	expected := *revision
	if expected < 0 {
		if preview {
			var current domain.WorkerEnvironment
			if err := c.request(ctx, "GET", path, nil, &current, false); err != nil {
				return err
			}
			expected = current.ConfigRevision
		} else {
			expected, err = c.revision(ctx, base, env, expected, true)
			if err != nil {
				return err
			}
		}
	}
	input["expected_revision"] = expected
	return c.call(ctx, "PATCH", path, input)
}

func (c *Client) configurations(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "--help" {
		_, err := fmt.Fprintln(c.Out, "Usage: krun workers configurations list|create|deploy [options]")
		return err
	}
	action, args := args[0], args[1:]
	f := flags("workers configurations "+action, c.Err)
	target := c.workerFlags(f)
	env := environmentFlag(f)
	var name, message, fromConfig, fromVersion, secretsFile string
	var configuration, build, version string
	var configRevision, deployRevision *int
	var key *string
	var bindings bindingsFlags
	switch action {
	case "create":
		f.StringVar(&name, "name", "", "Configuration snapshot name")
		f.StringVar(&message, "message", "", "Configuration snapshot description")
		f.StringVar(&fromConfig, "from-configuration", "", "Copy this configuration")
		f.StringVar(&fromVersion, "from-version", "", "Copy the configuration of this Worker version")
		f.StringVar(&secretsFile, "secrets-file", "", "JSON or YAML map of Secret changes (null removes a name)")
		bindings = workerBindingsFlags(f)
	case "deploy":
		f.StringVar(&configuration, "configuration", "", "Configuration snapshot ID")
		f.StringVar(&build, "build", "", "Successful build ID to use")
		f.StringVar(&version, "code-version", "", "Version ID to take code from")
		deployRevision = revisionFlag(f, "Expected deployment revision")
	case "list":
	default:
		return fmt.Errorf("unknown configurations command %q", action)
	}
	if action != "list" {
		configRevision = f.Int("configuration-revision", -1, "Expected configuration revision (default: read it)")
		key = idempotencyFlag(f)
	}
	base, err := target.path(f, args)
	if err != nil {
		return err
	}
	scope, err := environmentPath(base, *env)
	if err != nil {
		return err
	}
	path := scope + "/configurations"
	if action == "list" {
		return c.call(ctx, "GET", path, nil)
	}
	var input map[string]any
	if action == "create" {
		if name == "" || (fromConfig != "" && fromVersion != "") {
			return errors.New("--name is required; choose at most one copy source")
		}
		input, err = bindings.input(f)
		if err != nil {
			return err
		}
		if secretsFile != "" {
			var secrets map[string]*string
			if err := decodeInputFile(secretsFile, &secrets); err != nil || secrets == nil {
				return errors.New("could not read Secret file as a JSON or YAML map")
			}
			input["secrets"] = secrets
		}
		input["name"] = name
		input["message"] = message
		input["source_configuration_id"] = fromConfig
		input["source_version_id"] = fromVersion
	} else {
		if configuration == "" || (build == "") == (version == "") {
			return errors.New("--configuration and exactly one of --build or --code-version are required")
		}
		input = map[string]any{"build_id": build, "code_version_id": version}
		path += "/" + url.PathEscape(configuration) + "/deployments"
	}
	expectedConfig, expectedDeploy, err := c.configurationRevisions(ctx, base, *env, *configRevision, deployRevision)
	if err != nil {
		return err
	}
	input["expected_configuration_revision"] = expectedConfig
	fmt.Fprintln(c.Err, "Configuration revision:", expectedConfig)
	if action == "deploy" {
		input["expected_revision"] = expectedDeploy
		fmt.Fprintln(c.Err, "Deployment revision:", expectedDeploy)
	}
	input["idempotency_key"] = c.submissionKey(*key)
	return c.call(ctx, "POST", path, input)
}

func (c *Client) configurationRevisions(ctx context.Context, base, env string, config int, deploy *int) (int, int, error) {
	deployment := 0
	if deploy != nil {
		deployment = *deploy
	}
	if config >= 0 && deployment >= 0 {
		return config, deployment, nil
	}
	current, err := c.environment(ctx, base, env)
	if err != nil {
		return 0, 0, err
	}
	if config < 0 {
		config = current.ConfigRevision
	}
	if deployment < 0 {
		deployment = current.Revision
	}
	return config, deployment, nil
}
