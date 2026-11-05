package config

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
)

func (c *WorkersConfig) validateKubernetes() error {
	if c.KubernetesKubeconfigFile != "" {
		if !filepath.IsAbs(c.KubernetesKubeconfigFile) {
			return fmt.Errorf("kubernetes_kubeconfig_file must be an absolute path")
		}
		if c.KubernetesAPI != "" || c.KubernetesTokenFile != "" || c.KubernetesCAFile != "" {
			return fmt.Errorf("kubernetes_kubeconfig_file cannot be combined with kubernetes_api, kubernetes_token_file or kubernetes_ca_file")
		}
		return nil
	}
	if c.KubernetesContext != "" {
		return fmt.Errorf("kubernetes_context requires kubernetes_kubeconfig_file")
	}
	if c.KubernetesAPI == "" {
		c.KubernetesAPI = "https://kubernetes.default.svc"
	}
	u, err := url.Parse(c.KubernetesAPI)
	if err != nil || u.Scheme != "https" || u.Host == "" || (u.Path != "" && u.Path != "/") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("invalid Workers Kubernetes API")
	}
	c.KubernetesAPI = strings.TrimSuffix(c.KubernetesAPI, "/")
	if c.KubernetesTokenFile == "" {
		c.KubernetesTokenFile = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	}
	if c.KubernetesCAFile == "" {
		c.KubernetesCAFile = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
	}
	if !filepath.IsAbs(c.KubernetesTokenFile) || !filepath.IsAbs(c.KubernetesCAFile) {
		return fmt.Errorf("Workers Kubernetes credential files must be absolute paths")
	}
	return nil
}
