package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/krun-dev/krun/internal/domain"
)

const systemUsage = "Usage: krun system pool-manifests POOL_ID"

func (c *Client) system(ctx context.Context, args []string) error {
	if len(args) == 0 || (len(args) == 1 && (args[0] == "--help" || args[0] == "-h")) {
		fmt.Fprintln(c.Out, systemUsage)
		return nil
	}
	if args[0] == "pool-manifests" && len(args) == 2 && (args[1] == "--help" || args[1] == "-h") {
		fmt.Fprintln(c.Out, systemUsage)
		return nil
	}
	if len(args) != 2 || args[0] != "pool-manifests" || args[1] == "" {
		return errors.New(systemUsage)
	}
	var manifests domain.RuntimePoolManifests
	path := "/api/v1/system/config/runtime-pools/" + url.PathEscape(args[1]) + "/manifests"
	if err := c.request(ctx, http.MethodGet, path, nil, &manifests, false); err != nil {
		return err
	}
	_, err := fmt.Fprint(c.Out, manifests.YAML())
	return err
}
