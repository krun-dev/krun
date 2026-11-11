package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"time"

	"github.com/krun-dev/krun/internal/securetoken"
)

type releaseBuild struct {
	id                 string
	ref                string
	repositoryRevision int
	timeout            time.Duration
	localPath          string
	projectRoot        string
}

func (b *releaseBuild) flags(f *flag.FlagSet) {
	f.StringVar(&b.id, "build", "", "Reuse a successful build (default: build the bound repository)")
	f.StringVar(&b.ref, "ref", "", "Git ref for the repository build (default: the bound ref)")
	f.IntVar(&b.repositoryRevision, "repository-revision", -1, "Expected repository revision (default: read it)")
	f.DurationVar(&b.timeout, "build-timeout", 15*time.Minute, "Maximum time to wait for the build")
}

func (b releaseBuild) validate(f *flag.FlagSet) error {
	if flagSet(f, "path") {
		if err := requireID(b.localPath, "path"); err != nil {
			return err
		}
		if flagSet(f, "build") || flagSet(f, "ref") || flagSet(f, "repository-revision") {
			return errors.New("--build, --ref and --repository-revision cannot be combined with --path")
		}
	}
	if flagSet(f, "project-root") {
		if b.localPath == "" {
			return errors.New("--project-root requires --path")
		}
		if err := requireID(b.projectRoot, "project-root"); err != nil {
			return err
		}
	}
	if flagSet(f, "build") {
		if err := requireID(b.id, "build"); err != nil {
			return err
		}
		if flagSet(f, "ref") || flagSet(f, "repository-revision") || flagSet(f, "build-timeout") {
			return errors.New("--ref, --repository-revision and --build-timeout cannot be combined with --build")
		}
	}
	if b.timeout <= 0 {
		return errors.New("--build-timeout must be positive")
	}
	if flagSet(f, "repository-revision") && b.repositoryRevision < 0 {
		return errors.New("--repository-revision must be non-negative")
	}
	return nil
}

func (b releaseBuild) resolve(ctx context.Context, c *Client, action, base, env, releaseKey string) (string, error) {
	if b.id != "" {
		return b.id, nil
	}
	buildKey := "cli-build-" + securetoken.Digest(action+"\x00"+releaseKey)
	if b.localPath != "" {
		return b.resolveLocal(ctx, c, base, env, buildKey)
	}

	input := map[string]any{"environment": env}
	if err := c.repositoryBuildInput(ctx, base, b.ref, b.repositoryRevision, input); err != nil {
		return "", err
	}
	// Retrying a release with the same inputs must find the original build,
	// even if the branch has moved since that build was queued.
	fmt.Fprintln(c.Err, "Build idempotency key:", buildKey)
	input["idempotency_key"] = buildKey
	var build struct {
		ID string `json:"id"`
	}
	if err := c.request(ctx, "POST", base+"/builds", input, &build, false); err != nil {
		return "", err
	}
	return b.wait(ctx, c, base, build.ID, "repository")
}

func (b releaseBuild) wait(ctx context.Context, c *Client, base, id, source string) (string, error) {
	if id == "" {
		return "", errors.New("build response did not include a build ID")
	}
	fmt.Fprintln(c.Err, "Build ID:", id)
	_, err := c.waitForResult(ctx, base+"/builds/"+url.PathEscape(id), id, b.timeout, buildTerminal)
	if err != nil {
		return "", fmt.Errorf("%s build: %w; inspect with krun build logs --build %s", source, err, id)
	}
	return id, nil
}
