package config

import (
	"encoding/base64"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
)

type WorkerObjectStorageConfig struct {
	RequestTimeoutSeconds    int                 `json:"request_timeout_seconds"`
	CursorKey                string              `json:"cursor_key"`
	DefaultStore             string              `json:"default_store"`
	Stores                   []WorkerObjectStore `json:"stores"`
	CredentialTTLSeconds     int                 `json:"credential_ttl_seconds"`
	RefreshBeforeSeconds     int                 `json:"refresh_before_seconds"`
	CredentialCacheEntries   int                 `json:"credential_cache_entries"`
	CredentialConcurrency    int                 `json:"credential_concurrency"`
	CredentialTimeoutSeconds int                 `json:"credential_timeout_seconds"`
}

type WorkerObjectStore struct {
	ID              string                        `json:"id"`
	Endpoint        string                        `json:"endpoint"`
	Region          string                        `json:"region"`
	Bucket          string                        `json:"bucket"`
	RootPrefix      string                        `json:"root_prefix"`
	AddressingStyle string                        `json:"addressing_style"`
	Credentials     WorkerStorageCredentialConfig `json:"credentials"`
	Delivery        *WorkerObjectDeliveryConfig   `json:"delivery,omitempty"`
}

// CDN configuration must match the independently configured private origin,
// Type A SHA256 signing rule and its validity period.
type WorkerObjectDeliveryConfig struct {
	Type       string `json:"type"`
	BaseURL    string `json:"base_url"`
	SigningKey string `json:"signing_key"`
	TTLSeconds int    `json:"ttl_seconds"`
}

func (c *WorkerObjectDeliveryConfig) validate() error {
	if c == nil {
		return nil
	}
	u, err := url.Parse(c.BaseURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil ||
		u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return fmt.Errorf("object_storage delivery requires an HTTPS domain without a path")
	}
	if c.Type != "volc_cdn_a" || len(c.SigningKey) < 32 || len(c.SigningKey) > 256 ||
		strings.ContainsAny(c.SigningKey, "\r\n") || c.TTLSeconds < 60 || c.TTLSeconds > 3600 {
		return fmt.Errorf("invalid object_storage delivery provider, signing key or TTL")
	}
	return nil
}

type WorkerStorageCredentialConfig struct {
	Type            string `json:"type"`
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
	RoleTrn         string `json:"role_trn,omitempty"`
	STSEndpoint     string `json:"sts_endpoint,omitempty"`
}

func (c *WorkerObjectStorageConfig) Validate() error {
	if c == nil {
		return nil
	}
	if c.CursorKey != "" {
		key, err := base64.RawURLEncoding.DecodeString(c.CursorKey)
		if err != nil || len(key) != 32 {
			return fmt.Errorf("object_storage cursor_key must be a base64url encoded 32-byte key")
		}
	}
	if c.RequestTimeoutSeconds == 0 {
		c.RequestTimeoutSeconds = 600
	}
	if c.RequestTimeoutSeconds < 30 || c.RequestTimeoutSeconds > 3600 {
		return fmt.Errorf("object_storage request timeout must be between 30 and 3600 seconds")
	}
	if len(c.Stores) < 1 || len(c.Stores) > 32 {
		return fmt.Errorf("object_storage requires between 1 and 32 stores")
	}
	if c.CredentialTTLSeconds == 0 {
		c.CredentialTTLSeconds = 3600
	}
	if c.RefreshBeforeSeconds == 0 {
		c.RefreshBeforeSeconds = 300
	}
	if c.CredentialCacheEntries == 0 {
		c.CredentialCacheEntries = 1024
	}
	if c.CredentialConcurrency == 0 {
		c.CredentialConcurrency = 8
	}
	if c.CredentialTimeoutSeconds == 0 {
		c.CredentialTimeoutSeconds = 20
	}
	if c.CredentialTTLSeconds < 900 || c.CredentialTTLSeconds > 43200 ||
		c.RefreshBeforeSeconds < 30 || c.RefreshBeforeSeconds >= c.CredentialTTLSeconds/2 ||
		c.CredentialCacheEntries < 1 || c.CredentialCacheEntries > 65536 ||
		c.CredentialConcurrency < 1 || c.CredentialConcurrency > 128 ||
		c.CredentialTimeoutSeconds < 1 || c.CredentialTimeoutSeconds > 60 {
		return fmt.Errorf("invalid object_storage credential cache or lifetime settings")
	}
	seen := map[string]bool{}
	for index := range c.Stores {
		store := &c.Stores[index]
		if !Slug.MatchString(store.ID) || seen[store.ID] {
			return fmt.Errorf("invalid or duplicate object_storage store id")
		}
		seen[store.ID] = true
		if err := store.validate(); err != nil {
			return err
		}
	}
	if !seen[c.DefaultStore] {
		return fmt.Errorf("object_storage default_store must name a configured store")
	}
	return nil
}

func (s *WorkerObjectStore) validate() error {
	if err := s.Delivery.validate(); err != nil {
		return err
	}
	u, err := url.Parse(s.Endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" ||
		(u.Scheme != "https" && u.Scheme != "http") {
		return fmt.Errorf("invalid object_storage endpoint")
	}
	if !regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`).MatchString(s.Region) ||
		!regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`).MatchString(s.Bucket) {
		return fmt.Errorf("invalid object_storage region or bucket")
	}
	if !regexp.MustCompile(`^[A-Za-z0-9_-]+(?:/[A-Za-z0-9_-]+)*$`).MatchString(s.RootPrefix) || len(s.RootPrefix) > 160 {
		return fmt.Errorf("object_storage root_prefix must be an explicit safe namespace")
	}
	if s.AddressingStyle != "path" && s.AddressingStyle != "virtual" {
		return fmt.Errorf("object_storage addressing_style must be path or virtual")
	}
	if s.AddressingStyle == "virtual" && net.ParseIP(u.Hostname()) != nil {
		return fmt.Errorf("object_storage virtual addressing requires a DNS endpoint")
	}
	creds := &s.Credentials
	if creds.AccessKeyID == "" || creds.SecretAccessKey == "" {
		return fmt.Errorf("object_storage credential keys are required")
	}
	switch creds.Type {
	case "static":
		if creds.RoleTrn != "" || creds.STSEndpoint != "" {
			return fmt.Errorf("static object_storage credentials do not accept STS settings")
		}
	case "tos_assume_role":
		if u.Scheme != "https" || s.AddressingStyle != "virtual" ||
			u.Host != "tos-s3-"+s.Region+".volces.com" || strings.Contains(s.Bucket, ".") {
			return fmt.Errorf("TOS object_storage requires its regional HTTPS S3 endpoint and virtual addressing")
		}
		if !regexp.MustCompile(`^trn:iam::[0-9]+:role/[A-Za-z0-9+=,.@_-]{1,64}$`).MatchString(creds.RoleTrn) {
			return fmt.Errorf("invalid object_storage TOS role_trn")
		}
		if creds.STSEndpoint == "" {
			creds.STSEndpoint = "https://sts.volcengineapi.com"
		}
		if creds.STSEndpoint != "https://sts.volcengineapi.com" && creds.STSEndpoint != "https://sts."+s.Region+".volcengineapi.com" {
			return fmt.Errorf("invalid object_storage TOS sts_endpoint")
		}
	default:
		return fmt.Errorf("unsupported object_storage credential type")
	}
	return nil
}
