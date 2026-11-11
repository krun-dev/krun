package config

import (
	"errors"
	"net"
	"net/netip"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

type WorkerHyperdriveConfig struct {
	Listen       string `json:"listen"`
	Address      string `json:"address"`
	TLSCertFile  string `json:"tls_cert_file"`
	TLSKeyFile   string `json:"tls_key_file"`
	OriginCAFile string `json:"origin_ca_file,omitempty"`
}

var hyperdriveHost = regexp.MustCompile(`(?i)^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$`)

func (h *WorkerHyperdriveConfig) CIDR() string {
	host, _, _ := net.SplitHostPort(h.Listen)
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return ""
	}
	return netip.PrefixFrom(addr, addr.BitLen()).String()
}

func (c *Config) validateHyperdrive() error {
	if c.Workers == nil {
		return nil
	}
	if c.Deployment != nil {
		if c.Workers.Hyperdrive != nil {
			return errors.New("workers.hyperdrive is not supported by the Kubernetes deployment render yet; remove the section or use a host deployment")
		}
		return nil
	}
	if c.Workers.Hyperdrive == nil {
		return errors.New("workers.hyperdrive is required when workers are configured; PostgreSQL access is a built-in capability, see config/example.yaml")
	}
	h := c.Workers.Hyperdrive
	host, port, err := net.SplitHostPort(h.Listen)
	ip, ipErr := netip.ParseAddr(host)
	n, portErr := strconv.Atoi(port)
	if err != nil || ipErr != nil || ip.Is4In6() || ip.Zone() != "" ||
		!(ip.IsPrivate() || c.Development && ip.IsLoopback()) || portErr != nil || n < 1 || n > 65535 {
		return errors.New("workers.hyperdrive.listen requires a dedicated private IP and numeric TCP port")
	}
	advertisedHost, advertisedPort, err := net.SplitHostPort(h.Address)
	if err != nil || advertisedPort != port || len(advertisedHost) > 253 || !hyperdriveHost.MatchString(advertisedHost) {
		return errors.New("workers.hyperdrive.address requires a DNS hostname and the listener port")
	}
	for _, listener := range []string{c.Listen, c.InternalListen} {
		if listener == "" {
			continue
		}
		host, _, _ := net.SplitHostPort(listener)
		listenIP, err := netip.ParseAddr(host)
		if err != nil || listenIP.IsUnspecified() || listenIP.Unmap() == ip {
			return errors.New("HTTP listeners must use IPs distinct from the Hyperdrive listener")
		}
	}
	for _, cidr := range c.Workers.ProtectedEgressCIDRs {
		prefix, _ := netip.ParsePrefix(cidr)
		if prefix.Contains(ip) {
			return errors.New("workers.hyperdrive listener overlaps a protected egress CIDR")
		}
	}
	for _, file := range []string{h.TLSCertFile, h.TLSKeyFile} {
		if !filepath.IsAbs(file) {
			return errors.New("workers.hyperdrive TLS certificate and key require absolute file paths")
		}
	}
	if h.OriginCAFile != "" && !filepath.IsAbs(h.OriginCAFile) {
		return errors.New("workers.hyperdrive.origin_ca_file must be an absolute path")
	}
	if c.Workers.SecretsActiveKey == "" {
		return errors.New("workers.hyperdrive requires worker secret encryption")
	}
	h.Address = net.JoinHostPort(strings.ToLower(advertisedHost), port)
	return nil
}
