package kubernetes

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	appconfig "github.com/krun-dev/krun/internal/config"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

func newKubeconfigClient(config *appconfig.WorkersConfig) (*Client, error) {
	// Construct the loading rules directly: no KUBECONFIG, home-directory merge,
	// in-cluster fallback, or implicit cluster selection from the process host.
	rules := &clientcmd.ClientConfigLoadingRules{ExplicitPath: config.KubernetesKubeconfigFile}
	raw, err := rules.Load()
	if err != nil {
		return nil, fmt.Errorf("load Workers kubeconfig: %w", err)
	}
	contextName := config.KubernetesContext
	if contextName == "" {
		contextName = raw.CurrentContext
	}
	if contextName == "" || raw.Contexts[contextName] == nil {
		return nil, fmt.Errorf("Workers kubeconfig requires an existing selected context")
	}
	selected := clientcmd.NewNonInteractiveClientConfig(
		*raw,
		contextName,
		&clientcmd.ConfigOverrides{},
		rules,
	)
	settings, err := selected.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("configure Workers kubeconfig context: %w", err)
	}
	u, err := url.Parse(settings.Host)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("Workers kubeconfig server must be an HTTPS URL without credentials, query or fragment")
	}
	settings.Timeout = 10 * time.Second
	client, err := rest.HTTPClientFor(settings)
	if err != nil {
		return nil, fmt.Errorf("initialize Workers kubeconfig transport: %w", err)
	}
	return &Client{
		Base:           strings.TrimSuffix(settings.Host, "/"),
		TokenFile:      settings.BearerTokenFile,
		Client:         client,
		Namespace:      config.Namespace,
		NpmRegistry:    config.NPMRegistry,
		runtimeClass:   config.BuilderRuntimeClass,
		timeoutSeconds: config.BuilderTimeoutSeconds,
	}, nil
}
