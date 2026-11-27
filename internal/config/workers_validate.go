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
	if err := c.RuntimeConfig.Validate(); err != nil {
		return err
	}
	if origin, err := c.RuntimeAPIOrigin(); err != nil {
		return err
	} else if origin != nil {
		c.RuntimeAPIURL = strings.TrimSuffix(origin.String(), "/")
	}
	if err := c.ObjectStorage.Validate(); err != nil {
		return err
	}
	if c.GatewayRequestTimeoutSeconds == 0 {
		c.GatewayRequestTimeoutSeconds = 15
	}
	if c.GatewayRequestTimeoutSeconds < 1 || c.GatewayRequestTimeoutSeconds > 3600 {
		return fmt.Errorf("Workers gateway request timeout must be between 1 and 3600 seconds")
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
	if err := c.validateBuilderJob(); err != nil {
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
	if err := c.validateKubernetes(); err != nil {
		return err
	}
	dns := regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,180}$`)
	if e := c.ValidateCatalog(dns); e != nil {
		return e
	}
	if err := c.validateManagedOrigins(); err != nil {
		return err
	}
	if err := c.validateArtifactStorage(); err != nil {
		return err
	}
	if c.BuilderMode != "local" && c.BuilderImage == "" {
		return fmt.Errorf("Workers builder image required")
	}
	if err := c.ValidateSecretKeys(); err != nil {
		return err
	}
	if c.GradualAssetRetentionSeconds < 0 || c.GradualAssetRetentionSeconds > 90*24*60*60 {
		return fmt.Errorf("gradual_asset_retention_seconds must be between 0 and 7776000")
	}
	if c.PreviewLimit == 0 {
		c.PreviewLimit = 32
	}
	if c.PreviewLimit < 1 || c.PreviewLimit > 128 {
		return fmt.Errorf("preview_limit must be between 1 and 128")
	}
	if c.CloneDirectory != "" && !path.IsAbs(c.CloneDirectory) {
		return fmt.Errorf("clone_directory must be an absolute path")
	}
	if err := c.validateProtectedEgressCIDRs(); err != nil {
		return err
	}
	if c.DatabaseOriginCAFile != "" && !path.IsAbs(c.DatabaseOriginCAFile) {
		return fmt.Errorf("workers.database_origin_ca_file must be an absolute path")
	}
	if err := c.validateLocalDatabaseEgress(); err != nil {
		return err
	}
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
