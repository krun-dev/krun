package config

import (
	"fmt"
	"net"
	"net/url"
	"path"
	"regexp"
	slices "slices"
	"strings"
)

func (c *WorkersConfig) Validate() error {
	if c == nil {
		return nil
	}
	if err := c.ObjectStorage.Validate(); err != nil {
		return err
	}
	if c.ReconcileConcurrency == 0 {
		c.ReconcileConcurrency = 8
	}
	if c.ReconcileConcurrency < 1 || c.ReconcileConcurrency > 64 {
		return fmt.Errorf("reconcile_concurrency must be between 1 and 64")
	}

	if err := c.ValidateRepositoryProviders(); err != nil {
		return err
	}
	if c.BuilderPullPolicy == "" {
		c.BuilderPullPolicy = "IfNotPresent"
	}
	if !slices.Contains([]string{"Always", "IfNotPresent", "Never"}, c.BuilderPullPolicy) {
		return fmt.Errorf("invalid Workers builder pull policy")
	}
	if c.BuilderMode == "" {
		c.BuilderMode = "kubernetes"
	}
	if c.BuilderMode != "kubernetes" && c.BuilderMode != "local" {
		return fmt.Errorf("invalid Workers builder mode")
	}
	if c.BuilderMode == "local" && c.BuilderScript == "" {
		c.BuilderScript = "workers/builder/build-worker.mjs"
	}
	if c.BuilderRuntimeClass != "" {
		runtimeClass := regexp.MustCompile(`^[a-z0-9](?:[-a-z0-9.]{0,251}[a-z0-9])?$`)
		if !runtimeClass.MatchString(c.BuilderRuntimeClass) {
			return fmt.Errorf("invalid Workers builder runtime class")
		}
	}
	if c.BuilderTimeoutSeconds == 0 {
		c.BuilderTimeoutSeconds = 600
	}
	if c.BuilderTimeoutSeconds < 60 || c.BuilderTimeoutSeconds > 1800 {
		return fmt.Errorf("Workers builder timeout must be between 60 and 1800 seconds")
	}
	if c.NPMRegistry == "" {
		c.NPMRegistry = "https://registry.npmjs.org/"
	}
	registry, err := url.Parse(c.NPMRegistry)
	if err != nil || registry.Scheme != "https" || registry.Host == "" || registry.User != nil || registry.RawQuery != "" || registry.Fragment != "" {
		return fmt.Errorf("invalid Workers npm registry; use an HTTPS origin without embedded credentials")
	}
	if !strings.HasSuffix(registry.Path, "/") {
		registry.Path += "/"
	}
	c.NPMRegistry = registry.String()
	if c.S3LocationID == "" {
		c.S3LocationID = "primary"
	}
	if c.KubernetesAPI == "" {
		c.KubernetesAPI = "https://kubernetes.default.svc"
	}
	u, e := url.Parse(c.KubernetesAPI)
	if e != nil || u.Scheme != "https" || u.Host == "" || (u.Path != "" && u.Path != "/") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("invalid Workers Kubernetes API")
	}
	c.KubernetesAPI = strings.TrimSuffix(c.KubernetesAPI, "/")
	if c.KubernetesTokenFile == "" {
		c.KubernetesTokenFile = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	}
	if c.KubernetesCAFile == "" {
		c.KubernetesCAFile = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
	}
	if !path.IsAbs(c.KubernetesTokenFile) || !path.IsAbs(c.KubernetesCAFile) {
		return fmt.Errorf("Workers Kubernetes credential files must be absolute paths")
	}
	dns := regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,180}$`)
	if e := c.ValidateCatalog(dns); e != nil {
		return e
	}
	if c.S3Region == "" {
		c.S3Region = "us-east-1"
	}
	if !regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`).MatchString(c.S3Region) {
		return fmt.Errorf("invalid Workers S3 region")
	}
	if c.S3AddressingStyle == "" {
		c.S3AddressingStyle = "auto"
	}
	if c.S3AddressingStyle != "auto" && c.S3AddressingStyle != "path" && c.S3AddressingStyle != "virtual" {
		return fmt.Errorf("invalid Workers S3 addressing style")
	}
	if c.S3BucketMode == "" {
		c.S3BucketMode = "managed"
	}
	if c.S3BucketMode != "managed" && c.S3BucketMode != "external" {
		return fmt.Errorf("invalid Workers S3 bucket mode")
	}
	if c.S3AccessMode == "" {
		c.S3AccessMode = "direct"
		if c.S3BucketMode == "external" {
			c.S3AccessMode = "signed"
		}
	}
	if c.S3AccessMode != "direct" && c.S3AccessMode != "signed" {
		return fmt.Errorf("invalid Workers S3 access mode")
	}
	if c.S3Prefix != "" {
		hasUnsafeCharacters := strings.ContainsAny(c.S3Prefix, "\\?#%") ||
			strings.IndexFunc(c.S3Prefix, func(r rune) bool {
				return r <= 32 || r == 127
			}) >= 0
		invalidPath := len(c.S3Prefix) > 160 ||
			path.Clean(c.S3Prefix) != c.S3Prefix ||
			path.IsAbs(c.S3Prefix) ||
			strings.HasPrefix(c.S3Prefix, "../") ||
			c.S3Prefix == "." || c.S3Prefix == ".."
		if invalidPath || hasUnsafeCharacters {
			return fmt.Errorf("invalid Workers S3 prefix")
		}
	}

	u, e = url.Parse(c.S3Endpoint)
	if e != nil || u.Host == "" || u.Path != "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("invalid Workers S3 endpoint")
	}
	if c.S3BuilderEndpoint != "" {
		u, e := url.Parse(c.S3BuilderEndpoint)
		if e != nil || u.Host == "" || u.Path != "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
			return fmt.Errorf("invalid Workers S3 builder endpoint")
		}
	} else {
		c.S3BuilderEndpoint = c.S3Endpoint
	}
	if c.S3RuntimeEndpoint != "" {
		u, e := url.Parse(c.S3RuntimeEndpoint)
		if e != nil || u.Host == "" || u.Path != "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
			return fmt.Errorf("invalid Workers S3 runtime endpoint")
		}
	} else {
		c.S3RuntimeEndpoint = c.S3BuilderEndpoint
	}
	if c.S3AddressingStyle == "virtual" {
		for _, endpoint := range []string{c.S3Endpoint, c.S3BuilderEndpoint, c.S3RuntimeEndpoint} {
			u, _ := url.Parse(endpoint)
			if net.ParseIP(u.Hostname()) != nil {
				return fmt.Errorf("Workers S3 virtual addressing requires a DNS endpoint")
			}
		}
	}
	if (c.BuilderMode != "local" && c.BuilderImage == "") || c.S3AccessKey == "" || c.S3SecretKey == "" {
		return fmt.Errorf("Workers image and credentials required")
	}
	if err := c.ValidateSecretKeys(); err != nil {
		return err
	}
	if c.PreviewLimit == 0 {
		c.PreviewLimit = 32
	}
	if c.PreviewLimit < 1 || c.PreviewLimit > 128 {
		return fmt.Errorf("preview_limit must be between 1 and 128")
	}
	if c.StaticService == "" {
		c.StaticService = "static-assets"
	}
	if c.StaticServicePort == 0 {
		c.StaticServicePort = 9000
	}
	if !dns.MatchString(c.StaticService) {
		return fmt.Errorf("invalid Workers static service")
	}
	if c.CloneDirectory != "" && !path.IsAbs(c.CloneDirectory) {
		return fmt.Errorf("clone_directory must be an absolute path")
	}
	if c.ConfigBaseURL == "" {
		c.ConfigBaseURL = c.S3ObjectURL(c.S3RuntimeEndpoint, "config")
	}
	u, e = url.Parse(c.ConfigBaseURL)
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("invalid Workers config base URL")
	}
	c.ConfigBaseURL = strings.TrimSuffix(c.ConfigBaseURL, "/")
	if err := c.ValidateRuntimePools(dns); err != nil {
		return err
	}
	if err := c.ValidateNetworkProfiles(); err != nil {
		return err
	}
	for _, domain := range c.Domains {
		if _, err := c.DomainRuntimePool(domain.ID); err != nil {
			return err
		}
	}
	return nil
}
