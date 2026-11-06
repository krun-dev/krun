package config

import (
	"fmt"
	"net/url"
	"strconv"
)

// RuntimeAPIOrigin is nil when runtime traffic should use the cluster Service.
// This origin receives pool credentials and is infrastructure configuration.
func (c *WorkersConfig) RuntimeAPIOrigin() (*url.URL, error) {
	if c.RuntimeAPIURL == "" {
		return nil, nil
	}
	u, err := url.Parse(c.RuntimeAPIURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil ||
		(u.Path != "" && u.Path != "/") || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return nil, fmt.Errorf("Workers runtime_api_url must be an HTTPS origin without credentials, path, query or fragment")
	}
	if u.Port() != "" {
		port, err := strconv.Atoi(u.Port())
		if err != nil || port < 1 || port > 65535 {
			return nil, fmt.Errorf("Workers runtime_api_url has an invalid port")
		}
	}
	return u, nil
}
