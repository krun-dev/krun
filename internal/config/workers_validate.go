package config

import (
	"fmt"
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
	if c.ReconcileConcurrency == 0 {
		c.ReconcileConcurrency = 8
	}
	if c.ReconcileConcurrency < 1 || c.ReconcileConcurrency > 64 {
		return fmt.Errorf("reconcile_concurrency must be between 1 and 64")
	}

	if c == nil {
		return nil
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
		c.ConfigBaseURL = strings.TrimSuffix(c.S3Endpoint, "/") + "/" + c.S3Bucket + "/config"
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
	return nil
}
