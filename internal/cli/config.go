package cli

import (
	"errors"
	"flag"
	"fmt"
	"regexp"
	"strings"
)

const configHelp = `Usage:
  krun config set endpoint URL
  krun config set org ORG
  krun config get endpoint|org
  krun config unset org
  krun config show

Changing the endpoint clears saved credentials and the default organization.
--profile NAME selects a named profile; an explicit YAML path also works.
`

var organizationID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

func (c *Client) orgFlag(f *flag.FlagSet) *string {
	return f.String("org", c.Profile.Organization, "Organization short name or ID (overrides the profile default)")
}

func (c *Client) configure(args []string, saved Profile) error {
	if len(args) == 0 || args[0] == "--help" {
		_, err := fmt.Fprint(c.Out, configHelp)
		return err
	}

	// Config commands use the saved profile, independently of endpoint and token overrides.
	const usageError = "use krun config --help to see valid commands"
	var result string
	switch args[0] {
	case "show":
		if len(args) != 1 {
			return errors.New(usageError)
		}
		_, err := fmt.Fprintf(c.Out, "Config: %s\nEndpoint: %s\nOrganization: %s\nCredentials saved: %t\n",
			c.Path, saved.Endpoint, saved.Organization, saved.Token != "" || saved.RefreshToken != "")
		return err
	case "get":
		if len(args) != 2 {
			return errors.New(usageError)
		}
		switch args[1] {
		case "endpoint":
			result = saved.Endpoint
		case "org":
			result = saved.Organization
		default:
			return errors.New(usageError)
		}
		_, err := fmt.Fprintln(c.Out, result)
		return err
	case "set":
		if len(args) != 3 {
			return errors.New(usageError)
		}
		switch args[1] {
		case "endpoint":
			endpoint := strings.TrimRight(args[2], "/")
			if err := validateEndpoint(endpoint); err != nil {
				return err
			}
			previous := saved.Endpoint
			if previous == "" {
				previous = defaultEndpoint
			}
			if strings.TrimRight(previous, "/") != endpoint {
				saved.clearCredentials()
				saved.Organization = ""
			}
			saved.Endpoint = endpoint
			result = "Endpoint saved: " + endpoint
		case "org":
			if !organizationID.MatchString(args[2]) {
				return errors.New("use an organization short name or ID from krun org list")
			}
			saved.Organization = args[2]
			result = "Organization saved: " + saved.Organization
		default:
			return errors.New(usageError)
		}
	case "unset":
		if len(args) != 2 || args[1] != "org" {
			return errors.New(usageError)
		}
		if saved.Organization == "" {
			return nil
		}
		saved.Organization = ""
		result = "Default organization cleared."
	default:
		return errors.New(usageError)
	}

	c.Profile = saved
	if err := c.save(); err != nil {
		return err
	}
	_, err := fmt.Fprintln(c.Out, result)
	return err
}
