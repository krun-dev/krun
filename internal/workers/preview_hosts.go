package workers

import (
	"context"
	"regexp"
	"strings"

	appconfig "github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/domain"
)

type customPreviewDomain struct {
	host   string
	public bool
}

// Separate deployment hostnames from mutable Preview names, including names
// already present before custom Preview URLs were introduced.
var previewDeploymentLabel = regexp.MustCompile(`^d-[0-9a-f]{20}-`)

func customPreviewHost(base, name, deployment string) string {
	if name == appconfig.WorkerBaseline || name != strings.ToLower(name) ||
		!managedLabel.MatchString(name) || previewDeploymentLabel.MatchString(name) {
		return ""
	}
	label := name
	if deployment != "" {
		prefix := DeploymentEnvironment(deployment) + "-"
		label = prefix + strings.TrimRight(name[:min(len(name), 63-len(prefix))], "-")
	}
	host := label + "." + base
	if len(host) > 253 {
		return ""
	}
	return host
}

func (s *Service) CustomPreviewDomains(ctx context.Context, worker domain.Worker) ([]string, error) {
	bindings, err := s.Store.WorkerManagedHostnames(ctx, worker.Organization, worker.Worker)
	if err != nil {
		return nil, err
	}
	var hosts []string
	for _, item := range s.typedHostnames(bindings, appconfig.WorkerDomainCustom) {
		if len(item.Routes) == 1 && item.Routes[0].PreviewsEnabled &&
			s.Config.Workers.ManagedDomainAllowsWorker(item.SuffixID, worker.NetworkProfile) {
			hosts = append(hosts, item.Host)
		}
	}
	return hosts, nil
}

func CustomPreviewHosts(bases []string, name, deployment string) []string {
	var hosts []string
	for _, base := range bases {
		if host := customPreviewHost(base, name, deployment); host != "" {
			hosts = append(hosts, host)
		}
	}
	return hosts
}
