package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Keep this version in sync with the repository builder's pinned Wrangler.
const dryRunWrangler = "wrangler@4.155.0"

func (c *Client) deployDryRun(ctx context.Context, f *flag.FlagSet, outdir string) error {
	var invalid []string
	f.Visit(func(option *flag.Flag) {
		switch option.Name {
		case "dry-run", "outdir":
		default:
			invalid = append(invalid, "--"+option.Name)
		}
	})
	if len(invalid) > 0 {
		return fmt.Errorf("--dry-run uses the local checkout; cannot combine with %s", strings.Join(invalid, ", "))
	}
	config := c.configPath
	if config != "" {
		switch strings.ToLower(filepath.Ext(config)) {
		case ".json", ".jsonc", ".toml":
		default:
			return errors.New("--config requires a Wrangler .json, .jsonc or .toml file")
		}
	}

	args := []string{"--yes", dryRunWrangler, "deploy", "--dry-run"}
	for _, option := range []struct{ name, path string }{
		{"--config", config},
		{"--outdir", outdir},
	} {
		if option.path == "" {
			continue
		}
		path, err := filepath.Abs(option.path)
		if err != nil {
			return fmt.Errorf("%s: %w", option.name, err)
		}
		args = append(args, option.name, path)
	}
	cmd, err := dryRunCommand(ctx, args)
	if err != nil {
		return err
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = c.In, c.Out, c.Err
	cmd.Env = dryRunEnvironment(os.Environ())
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("local Wrangler dry run failed: %w", err)
	}
	return nil
}

func dryRunCommand(ctx context.Context, args []string) (*exec.Cmd, error) {
	npx, err := exec.LookPath("npx")
	if err != nil {
		return nil, errors.New("--dry-run requires Node.js and npm (npx)")
	}
	ext := strings.ToLower(filepath.Ext(npx))
	if runtime.GOOS == "windows" && (ext == ".cmd" || ext == ".bat") {
		// Run npm's script through Node so user paths never go through cmd.exe.
		node, err := exec.LookPath("node")
		if err != nil {
			return nil, errors.New("--dry-run requires Node.js on PATH")
		}
		script := filepath.Join(filepath.Dir(npx), "node_modules", "npm", "bin", "npx-cli.js")
		if info, err := os.Stat(script); err != nil || !info.Mode().IsRegular() {
			return nil, errors.New("--dry-run could not locate npm's npx script next to npx.cmd")
		}
		return exec.CommandContext(ctx, node, append([]string{script}, args...)...), nil
	}
	return exec.CommandContext(ctx, npx, args...), nil
}

func dryRunEnvironment(env []string) []string {
	var clean []string
	for _, item := range env {
		name, _, _ := strings.Cut(item, "=")
		name = strings.ToUpper(name)
		if strings.HasPrefix(name, "KRUN_") || strings.HasPrefix(name, "CLOUDFLARE_") ||
			strings.HasPrefix(name, "CF_") || strings.HasPrefix(name, "WRANGLER_") {
			continue
		}
		clean = append(clean, item)
	}
	return append(clean, "WRANGLER_SEND_METRICS=false")
}
