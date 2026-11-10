package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Version is set by the download build using -ldflags.
var Version = "dev"
var buildCommit = "unknown"
var publishedAt = "unreleased"

const maxUpgradeSize = 128 << 20

type cliRelease struct {
	Version string            `json:"version"`
	Files   map[string]string `json:"files"`
}

func cliDownloadName() (string, error) {
	if (runtime.GOOS != "darwin" && runtime.GOOS != "linux" && runtime.GOOS != "windows") ||
		(runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		return "", fmt.Errorf("unsupported platform: %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	name := "krun-" + runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name, nil
}

func (c *Client) upgrade(ctx context.Context, args []string) error {
	f := flags("upgrade", c.Err)
	check := f.Bool("check", false, "Check the published CLI without installing it")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return errors.New("upgrade does not accept positional arguments")
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return err
	}
	return c.upgradeExecutable(ctx, executable, *check)
}

func (c *Client) upgradeExecutable(ctx context.Context, executable string, check bool) error {
	if err := validateEndpoint(c.Profile.Endpoint); err != nil {
		return err
	}
	name, err := cliDownloadName()
	if err != nil {
		return err
	}
	version, expected, err := c.publishedCLI(ctx, name)
	if err != nil {
		return err
	}
	current, err := os.Open(executable)
	if err != nil {
		return err
	}
	hash := sha256.New()
	_, err = io.Copy(hash, current)
	current.Close()
	if err != nil {
		return err
	}
	if hex.EncodeToString(hash.Sum(nil)) == expected {
		fmt.Fprintln(c.Out, "krun is up to date:", version)
		return nil
	}
	if check {
		fmt.Fprintf(c.Out, "Installed: %s\nPublished: %s\nRun krun upgrade to install the published CLI.\n", Version, version)
		return nil
	}
	if err := c.installCLI(ctx, executable, name, expected); err != nil {
		return err
	}
	fmt.Fprintf(c.Out, "Upgraded krun to %s (%s).\n", version, executable)
	return nil
}

func (c *Client) publishedCLI(ctx context.Context, name string) (string, string, error) {
	response, err := c.downloadCLI(ctx, "manifest.json")
	if err != nil {
		return "", "", err
	}
	var release cliRelease
	err = json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&release)
	response.Body.Close()
	if err != nil {
		return "", "", fmt.Errorf("invalid CLI manifest: %w", err)
	}
	expected := release.Files[name]
	digest, err := hex.DecodeString(expected)
	if strings.TrimSpace(release.Version) == "" || err != nil || len(digest) != sha256.Size {
		return "", "", fmt.Errorf("CLI manifest has no valid release for %s", name)
	}
	return release.Version, strings.ToLower(expected), nil
}

// Downloads are public static files. Never attach API credentials or follow
// redirects to another origin, even when the API client has a custom transport.
func (c *Client) downloadCLI(ctx context.Context, name string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.Profile.Endpoint, "/")+"/downloads/"+name, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Cache-Control", "no-cache")
	client := http.Client{Timeout: 2 * time.Minute}
	if c.HTTP != nil {
		client.Transport = c.HTTP.Transport
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return errors.New("CLI download redirects refused")
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return nil, fmt.Errorf("CLI download %s returned HTTP %d; ensure the console publishes its CLI downloads", name, response.StatusCode)
	}
	return response, nil
}

func (c *Client) installCLI(ctx context.Context, executable, name, expected string) error {
	staged, err := os.CreateTemp(filepath.Dir(executable), ".krun-upgrade-*")
	if err != nil {
		return fmt.Errorf("cannot write the CLI installation directory: %w", err)
	}
	defer os.Remove(staged.Name())
	defer staged.Close()
	response, err := c.downloadCLI(ctx, name)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(staged, hash), io.LimitReader(response.Body, maxUpgradeSize+1))
	if err != nil {
		return fmt.Errorf("download CLI: %w", err)
	}
	if size == 0 || size > maxUpgradeSize {
		return errors.New("invalid CLI download size")
	}
	if hex.EncodeToString(hash.Sum(nil)) != expected {
		return errors.New("CLI checksum mismatch; installed executable was not changed, retry after the release finishes publishing")
	}
	if err := staged.Chmod(0755); err != nil {
		return err
	}
	if err := staged.Sync(); err != nil {
		return err
	}
	if err := staged.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return replaceCLI(staged.Name(), executable)
}
