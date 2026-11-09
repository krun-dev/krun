package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/krun-dev/krun/internal/domain"
)

const secretHelp = `Usage: krun secret COMMAND [--org ID] --worker ID
  list         List Secret names
  put NAME     Set a Secret from standard input or --file
  delete NAME  Remove a Secret
  bulk FILE    Set multiple Secrets from a JSON or YAML string map
Use --env NAME or --previews-base to choose the target.
`

func (c *Client) secret(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "--help" {
		_, err := fmt.Fprint(c.Out, secretHelp)
		return err
	}
	action, args := args[0], args[1:]
	f := flags("secret "+action, c.Err)
	target := c.workerFlags(f)
	env := f.String("env", "", "Target environment")
	preview := f.Bool("previews-base", false, "Apply to defaults for new previews")
	var valueFile, name, bulkFile string
	var revision *int
	var key *string
	var stage *bool
	switch action {
	case "list":
	case "put", "delete", "bulk":
		revision = revisionFlag(f, "Expected deployment revision")
		key = idempotencyFlag(f)
		stage = f.Bool("stage", false, "Save the new version without deploying it")
		operandName := "NAME"
		if action == "bulk" {
			bulkFile, args = operand(args)
			operandName = "FILE"
		} else {
			name, args = operand(args)
		}
		f.Usage = func() {
			fmt.Fprintf(c.Err, "Usage: krun secret %s %s [options]\n", action, operandName)
			f.PrintDefaults()
		}
		if action == "put" {
			f.StringVar(&valueFile, "file", "", "Read the Secret value from this file")
		}
	default:
		return fmt.Errorf("unknown secret command %q; use krun secret --help", action)
	}

	base, err := target.path(f, args)
	if err != nil {
		return err
	}
	if *preview == (*env != "") {
		return errors.New("choose exactly one of --env or --previews-base")
	}
	scope := base + "/previews-base"
	if !*preview {
		scope, err = environmentPath(base, *env)
		if err != nil {
			return err
		}
	}
	if action == "list" {
		if !*preview {
			return c.call(ctx, "GET", scope+"/secrets", nil)
		}
		var current domain.WorkerEnvironment
		if err := c.request(ctx, "GET", scope, nil, &current, false); err != nil {
			return err
		}
		names := current.SecretNames
		items := make([]map[string]string, 0, len(names))
		for _, name := range names {
			items = append(items, map[string]string{"name": name})
		}
		return c.outputJSON(map[string]any{"items": items})
	}

	secrets := map[string]*string{}
	if action == "bulk" {
		if err := requireOperand(bulkFile, "FILE"); err != nil {
			return err
		}
		if err := decodeInputFile(bulkFile, &secrets); err != nil {
			return errors.New("could not read Secret file as a JSON or YAML string map")
		}
		if len(secrets) == 0 || len(secrets) > 100 {
			return errors.New("Secret file must contain between 1 and 100 entries")
		}
		for _, value := range secrets {
			if value == nil || len(*value) > 16<<10 {
				return errors.New("each Secret must be a string of at most 16 KiB")
			}
		}
	} else {
		if err := requireOperand(name, "NAME"); err != nil {
			return err
		}
		secrets[name] = nil
		if action == "put" {
			value, err := c.readSecret(valueFile)
			if err != nil {
				return err
			}
			secrets[name] = &value
		}
	}
	if *preview && (*stage || *revision != -1) {
		return errors.New("--stage and --revision require an environment")
	}
	expected := 0
	if !*preview {
		expected, err = c.revision(ctx, base, *env, *revision, false)
		if err != nil {
			return err
		}
	}
	fmt.Fprintln(c.Err, "Deployment revision:", expected)
	input := map[string]any{
		"secrets": secrets, "deploy": !*stage, "expected_revision": expected,
		"idempotency_key": c.submissionKey(*key),
	}
	return c.call(ctx, "PATCH", scope+"/secrets", input)
}

func (c *Client) readSecret(path string) (string, error) {
	const limit = 16 << 10
	if path != "" {
		value, err := readInputFile(path, limit)
		return string(value), err
	}
	if file, ok := c.In.(*os.File); ok {
		info, err := file.Stat()
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeCharDevice != 0 {
			return "", errors.New("pipe the Secret value into stdin or provide --file")
		}
	}
	value, err := readLimited(c.In, limit)
	return string(value), err
}
