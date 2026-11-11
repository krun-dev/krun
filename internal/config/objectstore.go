package config

import (
	"net"
	"net/url"
	"strings"
)

// S3Key maps a logical key into this installation's isolated storage prefix.
func (c *WorkersConfig) S3Key(key string) string {
	if c.S3Prefix == "" {
		return key
	}
	return c.S3Prefix + "/" + key
}

func (c *WorkersConfig) S3ObjectURL(endpoint, key string) string {
	u, err := url.Parse(endpoint)
	if err != nil {
		return ""
	}
	if c.S3UsesVirtualHost(*u, c.S3Bucket) {
		u.Host = c.S3Bucket + "." + u.Host
		u.Path = "/" + c.S3Key(key)
	} else {
		u.Path = "/" + c.S3Bucket + "/" + c.S3Key(key)
	}
	return u.String()
}

// S3UsesVirtualHost is shared by SDK requests and runtime object URLs. Unknown
// S3-compatible endpoints default to path style; providers can explicitly select
// virtual style without introducing a provider-specific client.
func (c *WorkersConfig) S3UsesVirtualHost(endpoint url.URL, bucket string) bool {
	if c.S3AddressingStyle == "virtual" {
		return true
	}
	if c.S3AddressingStyle == "path" {
		return false
	}
	if net.ParseIP(endpoint.Hostname()) != nil ||
		(endpoint.Scheme == "https" && strings.Contains(bucket, ".")) {
		return false
	}

	host := strings.ToLower(endpoint.Hostname())
	return strings.HasSuffix(host, ".amazonaws.com") ||
		strings.HasSuffix(host, ".amazonaws.com.cn")
}
