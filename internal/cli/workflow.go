package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/securetoken"
)

type workerScope struct {
	org    *string
	worker *string
}

func (c *Client) workerFlags(f *flag.FlagSet) workerScope {
	return workerScope{
		org:    c.orgFlag(f),
		worker: f.String("worker", "", "Worker ID, locator or full resource path"),
	}
}

func parseFlags(f *flag.FlagSet, args []string) error {
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	return nil
}

func organizationPath(org string) (string, error) {
	if org == "" {
		return "", errors.New("specify --org or set a default with krun config set org")
	}
	return "/api/v1/orgs/" + url.PathEscape(org), nil
}

func (s workerScope) path(f *flag.FlagSet, args []string) (string, error) {
	if err := parseFlags(f, args); err != nil {
		return "", err
	}
	return s.resolvedPath(f)
}

func (s workerScope) resolvedPath(f *flag.FlagSet) (string, error) {
	if err := resolveResourcePath(f, "workers", s.org, s.worker); err != nil {
		return "", err
	}
	org, err := organizationPath(*s.org)
	if err != nil {
		return "", err
	}
	if *s.worker == "" {
		return "", errors.New("--worker is required")
	}
	return org + "/workers/" + url.PathEscape(*s.worker), nil
}

func resolveResourcePath(f *flag.FlagSet, collection string, org, resource *string) error {
	if !strings.Contains(*resource, "/") {
		return nil
	}
	parts := strings.Split(*resource, "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] != collection || parts[2] == "" {
		return errors.New("invalid resource path")
	}
	if flagSet(f, "org") && *org != parts[0] {
		return errors.New("--org conflicts with the organization in the resource path")
	}
	*org, *resource = parts[0], parts[2]
	return nil
}

func environmentFlag(f *flag.FlagSet) *string {
	return f.String("env", "", "Target environment name (required)")
}

func environmentPath(base, environment string) (string, error) {
	if environment == "" {
		return "", errors.New("--env is required")
	}
	return base + "/environments/" + url.PathEscape(environment), nil
}

func (c *Client) environment(ctx context.Context, base, name string) (domain.WorkerEnvironment, error) {
	var result struct {
		Items []domain.WorkerEnvironment `json:"items"`
	}
	if err := c.request(ctx, "GET", base+"/environments", nil, &result, false); err != nil {
		return domain.WorkerEnvironment{}, err
	}
	for _, env := range result.Items {
		if env.Name == name {
			return env, nil
		}
	}
	return domain.WorkerEnvironment{}, fmt.Errorf("environment %q is unavailable", name)
}

func idempotencyFlag(f *flag.FlagSet) *string {
	return f.String("idempotency-key", "", "Reuse this key with identical inputs to retry submission")
}

func (c *Client) submissionKey(key string) string {
	if key == "" {
		key = securetoken.NewID("cli")
	}
	fmt.Fprintln(c.Err, "Idempotency key:", key)
	return key
}

func (c *Client) revision(ctx context.Context, base, env string, revision int, config bool) (int, error) {
	if revision >= 0 {
		return revision, nil
	}
	current, err := c.environment(ctx, base, env)
	if err != nil {
		return 0, err
	}
	if config {
		return current.ConfigRevision, nil
	}
	return current.Revision, nil
}

func revisionFlag(f *flag.FlagSet, description string) *int {
	return f.Int("revision", -1, description+" (default: read the current revision)")
}

func requireID(id, name string) error {
	if id == "" {
		return fmt.Errorf("--%s is required", name)
	}
	return nil
}

func (c *Client) outputJSON(value any) error {
	encoder := json.NewEncoder(c.Out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func (c *Client) waitFor(ctx context.Context, path, id string, timeout time.Duration, terminal func(string) (bool, bool)) error {
	raw, waitErr := c.waitForResult(ctx, path, id, timeout, terminal)
	if raw != nil {
		if err := c.outputJSON(raw); err != nil {
			return err
		}
	}
	return waitErr
}

func (c *Client) waitForResult(ctx context.Context, path, id string, timeout time.Duration, terminal func(string) (bool, bool)) (json.RawMessage, error) {
	if timeout <= 0 {
		return nil, errors.New("wait timeout must be positive")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	var lastState string
	for {
		var raw json.RawMessage
		if err := c.request(ctx, "GET", path, nil, &raw, false); err != nil {
			return nil, fmt.Errorf("wait for %s: %w", id, err)
		}
		var result struct {
			State string `json:"state"`
			Error string `json:"error"`
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			return nil, err
		}
		if result.State == "" {
			return nil, fmt.Errorf("wait for %s: missing state", id)
		}
		if result.State != lastState {
			fmt.Fprintf(c.Err, "%s: %s\n", id, result.State)
			lastState = result.State
		}
		done, success := terminal(result.State)
		if done {
			if success {
				return raw, nil
			}
			return raw, fmt.Errorf("%s ended in %s%s", id, result.State, stateDetail(result.Error))
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("wait for %s (last state %s): %w", id, lastState, ctx.Err())
		case <-ticker.C:
		}
	}
}

func stateDetail(detail string) string {
	if detail == "" {
		return ""
	}
	return ": " + strings.TrimSpace(detail)
}

func operand(args []string) (string, []string) {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		return args[0], args[1:]
	}
	return "", args
}

func requireOperand(value, name string) error {
	if value == "" {
		return fmt.Errorf("%s is required before the options", name)
	}
	return nil
}
