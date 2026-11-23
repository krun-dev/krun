package config

import (
	"fmt"
	"net"
	"net/url"
	"path"
	"regexp"
	"strings"
)

// WorkerArtifactStorageConfig defines one physical store. Endpoint overrides
// point to the same bucket, from networks with different DNS or routing.
type WorkerArtifactStorageConfig struct {
	Endpoint        string                     `json:"endpoint"`
	BuilderEndpoint string                     `json:"builder_endpoint,omitempty"`
	RuntimeEndpoint string                     `json:"runtime_endpoint,omitempty"`
	LogReadEndpoint string                     `json:"log_read_endpoint,omitempty"`
	Region          string                     `json:"region"`
	Bucket          string                     `json:"bucket"`
	Prefix          string                     `json:"prefix,omitempty"`
	LocationID      string                     `json:"location_id"`
	AddressingStyle string                     `json:"addressing_style,omitempty"`
	BucketMode      string                     `json:"bucket_mode,omitempty"`
	ConfigBaseURL   string                     `json:"config_base_url,omitempty"`
	Credentials     ArtifactStorageCredentials `json:"credentials"`
}

type ArtifactStorageCredentials struct {
	Mode            string `json:"mode"`
	Provider        string `json:"provider,omitempty"`
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
	RoleTRN         string `json:"role_trn,omitempty"`
	STSEndpoint     string `json:"sts_endpoint,omitempty"`
}

func (c *WorkersConfig) ArtifactUsesSTS() bool {
	return c.ArtifactStorage.Credentials.Mode == "sts"
}

func (c *WorkersConfig) ArtifactCredentials() WorkerStorageCredentialConfig {
	auth := c.ArtifactStorage.Credentials
	switch auth.Mode {
	case "sts":
		if auth.Provider == "volcengine" {
			return WorkerStorageCredentialConfig{
				Type: "tos_assume_role", AccessKeyID: auth.AccessKeyID, SecretAccessKey: auth.SecretAccessKey,
				RoleTrn: auth.RoleTRN, STSEndpoint: auth.STSEndpoint,
			}
		}
	case "ak_sk":
		return WorkerStorageCredentialConfig{Type: "static", AccessKeyID: auth.AccessKeyID, SecretAccessKey: auth.SecretAccessKey}
	}
	return WorkerStorageCredentialConfig{}
}

func (s *WorkerArtifactStorageConfig) validateCredentials() error {
	auth := s.Credentials
	if auth.AccessKeyID == "" || auth.SecretAccessKey == "" {
		return fmt.Errorf("workers.artifact_storage.credentials requires access_key_id and secret_access_key")
	}
	switch auth.Mode {
	case "ak_sk":
		if auth.Provider != "" || auth.RoleTRN != "" || auth.STSEndpoint != "" {
			return fmt.Errorf("workers.artifact_storage.credentials mode ak_sk does not accept STS settings")
		}
	case "sts":
		if auth.Provider != "volcengine" {
			return fmt.Errorf("workers.artifact_storage.credentials mode sts requires provider volcengine")
		}
		if s.BucketMode == "" {
			s.BucketMode = "external"
		}
		if s.AddressingStyle == "" {
			s.AddressingStyle = "virtual"
		}
	default:
		return fmt.Errorf("workers.artifact_storage.credentials mode must be ak_sk or sts")
	}
	return nil
}

func (c *WorkersConfig) validateArtifactStorage() error {
	s := &c.ArtifactStorage
	if err := s.validateCredentials(); err != nil {
		return err
	}
	if s.LocationID == "" {
		s.LocationID = "primary"
	}
	if s.Region == "" {
		s.Region = "us-east-1"
	}
	if !regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`).MatchString(s.Region) {
		return fmt.Errorf("invalid Workers S3 region")
	}
	if s.AddressingStyle == "" {
		s.AddressingStyle = "auto"
	}
	if s.AddressingStyle != "auto" && s.AddressingStyle != "path" && s.AddressingStyle != "virtual" {
		return fmt.Errorf("invalid Workers S3 addressing style")
	}
	if s.BucketMode == "" {
		s.BucketMode = "managed"
	}
	if s.BucketMode != "managed" && s.BucketMode != "external" {
		return fmt.Errorf("invalid Workers S3 bucket mode")
	}
	if s.Prefix != "" {
		hasUnsafeCharacters := strings.ContainsAny(s.Prefix, "\\?#%*[]") ||
			strings.IndexFunc(s.Prefix, func(r rune) bool { return r <= 32 || r == 127 }) >= 0
		if hasUnsafeCharacters || len(s.Prefix) > 160 || path.Clean(s.Prefix) != s.Prefix ||
			path.IsAbs(s.Prefix) || strings.HasPrefix(s.Prefix, "../") || s.Prefix == "." || s.Prefix == ".." {
			return fmt.Errorf("invalid Workers S3 prefix")
		}
	}
	if s.BuilderEndpoint == "" {
		s.BuilderEndpoint = s.Endpoint
	}
	if s.LogReadEndpoint == "" {
		s.LogReadEndpoint = s.Endpoint
	}
	if s.RuntimeEndpoint == "" {
		s.RuntimeEndpoint = s.BuilderEndpoint
	}

	if c.ArtifactUsesSTS() && s.BucketMode != "external" {
		return fmt.Errorf("workers.artifact_storage in STS mode requires an existing bucket (bucket_mode: external)")
	}
	for _, item := range []struct{ field, endpoint string }{
		{"endpoint", s.Endpoint},
		{"builder_endpoint", s.BuilderEndpoint},
		{"runtime_endpoint", s.RuntimeEndpoint},
		{"log_read_endpoint", s.LogReadEndpoint},
	} {
		u, err := url.Parse(item.endpoint)
		if err != nil || !storageHTTPURL(u) || u.Path != "" {
			return fmt.Errorf("invalid workers.artifact_storage.%s", item.field)
		}
		if s.AddressingStyle == "virtual" && net.ParseIP(u.Hostname()) != nil {
			return fmt.Errorf("Workers S3 virtual addressing requires a DNS endpoint")
		}
		if c.ArtifactUsesSTS() {
			if err := c.ValidateArtifactSTSEndpoint(item.endpoint); err != nil {
				return fmt.Errorf("workers.artifact_storage.%s: %w", item.field, err)
			}
		}
	}

	if s.ConfigBaseURL == "" {
		s.ConfigBaseURL = c.S3ObjectURL(s.RuntimeEndpoint, "config")
	}
	u, err := url.Parse(s.ConfigBaseURL)
	if err != nil || !storageHTTPURL(u) {
		return fmt.Errorf("invalid workers.artifact_storage.config_base_url")
	}
	s.ConfigBaseURL = strings.TrimSuffix(s.ConfigBaseURL, "/")
	return nil
}

func storageHTTPURL(u *url.URL) bool {
	return u.Hostname() != "" && u.User == nil && u.RawQuery == "" && !u.ForceQuery &&
		u.Fragment == "" && (u.Scheme == "https" || u.Scheme == "http")
}

func (c *WorkersConfig) ValidateArtifactSTSEndpoint(endpoint string) error {
	store := c.artifactCredentialStore(endpoint, c.ArtifactCredentials())
	if err := store.validate(); err != nil {
		return fmt.Errorf("invalid artifact STS configuration: %w", err)
	}
	return nil
}

func (c *WorkersConfig) artifactCredentialStore(endpoint string, creds WorkerStorageCredentialConfig) WorkerObjectStore {
	u, err := url.Parse(endpoint)
	style := "path"
	if err == nil && c.S3UsesVirtualHost(*u, c.ArtifactStorage.Bucket) {
		style = "virtual"
	}
	return WorkerObjectStore{
		Endpoint: endpoint, Region: c.ArtifactStorage.Region,
		Bucket: c.ArtifactStorage.Bucket, RootPrefix: c.ArtifactStorage.Prefix,
		AddressingStyle: style, Credentials: creds,
	}
}
