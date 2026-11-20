package workers

import (
	"strings"
	"time"

	"github.com/krun-dev/krun/internal/authorization"
	appconfig "github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/storage"
)

func QueueVersionURL(st *domain.State, p authorization.Principal, v domain.WorkerVersion) domain.WorkerOperation {
	return queueWorkerDeployment(st, p, domain.WorkerOperation{
		Kind: domain.WorkerVersionURLOperation, Organization: v.Organization, Worker: v.Worker,
		Environment: v.Environment, Version: v.ID, ConfigRevision: v.ConfigRevision,
	}, "", nil)
}

// VersionURLSuffixes uses the prefix format Wrangler expects when it builds a
// Version URL from the first eight characters of a Version ID.
func VersionURLSuffixes(config *appconfig.WorkersConfig, worker domain.Worker) []string {
	if worker.PreviewURLsDisabled {
		return nil
	}
	var suffixes []string
	for _, pattern := range WorkerHostPatterns(worker, config) {
		slug, suffix, _ := strings.Cut(pattern, "\x00")
		if len(slug)+9 <= 63 && len(slug)+len(suffix)+10 <= 253 {
			suffixes = append(suffixes, "-"+slug+"."+suffix)
		}
	}
	return suffixes
}

func VersionURLHosts(config *appconfig.WorkersConfig, worker domain.Worker, version domain.WorkerVersion, now time.Time) []string {
	if !version.VersionURL || len(version.ID) < 8 || !version.Created.Add(storage.WorkerArtifactRetain).After(now) {
		return nil
	}
	var hosts []string
	for _, suffix := range VersionURLSuffixes(config, worker) {
		hosts = append(hosts, version.ID[:8]+suffix)
	}
	return hosts
}

func (s *Service) versionView(worker domain.Worker, v domain.WorkerVersion) domain.WorkerVersion {
	v = PublicWorkerVersion(v)
	if worker.Environments[v.Environment].Enabled {
		v.PreviewHosts = VersionURLHosts(s.Config.Workers, worker, v, time.Now())
	}
	return v
}
