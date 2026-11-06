package cli

import (
	"errors"
	"fmt"
	"strings"
)

const configHelp = `Usage:
  krun config set endpoint URL
  krun config get endpoint
  krun config show

The endpoint is the console origin, without /api. Changing it clears saved credentials.
--config FILE selects the profile to edit.
`

func (c *Client) configure(args []string) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(c.Out, configHelp)
		return nil
	}
	if len(args) == 1 && args[0] == "show" {
		// Reload the saved profile so temporary flags and environment overrides
		// never appear as persistent configuration or get written back to disk.
		profile, err := loadProfile(c.Path)
		if err != nil {
			return err
		}
		fmt.Fprintf(c.Out, "Config: %s\nEndpoint: %s\nCredentials saved: %t\n", c.Path, profile.Endpoint, profile.Token != "" || profile.RefreshToken != "")
		return nil
	}
	if len(args) < 2 || args[1] != "endpoint" {
		return errors.New("use config set endpoint URL, config get endpoint or config show")
	}
	profile, err := loadProfile(c.Path)
	if err != nil {
		return err
	}
	switch {
	case args[0] == "get" && len(args) == 2:
		fmt.Fprintln(c.Out, profile.Endpoint)
		return nil
	case args[0] == "set" && len(args) == 3:
		endpoint := strings.TrimRight(args[2], "/")
		if err := validateEndpoint(endpoint); err != nil {
			return err
		}
		c.Profile = profile
		if strings.TrimRight(profile.Endpoint, "/") != endpoint {
			c.clearCredentials()
		}
		c.Profile.Endpoint = endpoint
		if err := c.save(); err != nil {
			return err
		}
		fmt.Fprintln(c.Out, "Endpoint saved:", endpoint)
		return nil
	default:
		return errors.New("use config set endpoint URL or config get endpoint")
	}
}
