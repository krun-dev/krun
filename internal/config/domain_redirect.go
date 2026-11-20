package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

type DomainRedirect struct {
	IPs       []string `json:"ips"`
	TargetURL string   `json:"target_url"`
}

func (c *Config) validateDomainRedirect(source *url.URL) error {
	redirect := c.DomainRedirect
	if redirect == nil {
		return nil
	}
	if strings.TrimSpace(c.StaticDir) == "" {
		return errors.New("domain_redirect requires static_dir")
	}
	if len(c.TrustedProxyCIDRs) == 0 {
		return errors.New("domain_redirect requires trusted_proxy_cidrs")
	}
	if len(redirect.IPs) == 0 {
		return errors.New("domain_redirect.ips requires at least one IP address")
	}
	for _, value := range redirect.IPs {
		if net.ParseIP(value) == nil {
			return fmt.Errorf("invalid domain_redirect.ips entry %q", value)
		}
	}

	target, err := url.Parse(redirect.TargetURL)
	if err != nil || target.Hostname() == "" || target.User != nil ||
		(target.Path != "" && target.Path != "/") || target.RawPath != "" ||
		target.RawQuery != "" || target.ForceQuery || strings.Contains(redirect.TargetURL, "#") {
		return errors.New("domain_redirect.target_url must be an origin without credentials, path, query or fragment")
	}
	hostname := strings.TrimSuffix(target.Hostname(), ".")
	if target.Scheme != "https" {
		loopback := strings.EqualFold(hostname, "localhost") || net.ParseIP(hostname).IsLoopback()
		if !c.Development || target.Scheme != "http" || !loopback {
			return errors.New("domain_redirect.target_url requires HTTPS; development HTTP must use loopback")
		}
	}
	if target.Port() != "" {
		port, err := strconv.Atoi(target.Port())
		if err != nil || port < 1 || port > 65535 {
			return errors.New("domain_redirect.target_url has an invalid port")
		}
	}
	if strings.EqualFold(hostname, strings.TrimSuffix(source.Hostname(), ".")) {
		return errors.New("domain_redirect.target_url must use a different hostname from public_url")
	}
	return nil
}
