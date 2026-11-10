package workers

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/krun-dev/krun/internal/authorization"
	appconfig "github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/securetoken"
)

const DeploymentRetention = 30 * 24 * time.Hour

func RememberDeployment(st *domain.State, op domain.WorkerOperation) {
	if st.Workers.Deployments == nil {
		st.Workers.Deployments = map[string]domain.WorkerDeployment{}
	}
	id := DeploymentID(op.ID)
	st.Workers.Deployments[id] = domain.WorkerDeployment{ID: id, Organization: op.Organization, Worker: op.Worker, Environment: op.Environment, Version: op.Version, Operation: op.ID, Created: op.Created, Expires: op.Created.Add(DeploymentRetention)}
}
func DeploymentID(operation string) string {
	return "dep-" + strings.TrimPrefix(operation, "op-")
}

func DeploymentEnvironment(id string) string { return "d-" + securetoken.Digest(id)[:20] }
func DeploymentHosts(config *appconfig.WorkersConfig, worker domain.Worker, id string) []string {
	hosts := []string{}
	for _, pattern := range WorkerHostPatterns(worker, config.Domains) {
		if host := EnvHostName(pattern, DeploymentEnvironment(id)); host != "" {
			hosts = append(hosts, host)
		}
	}
	return hosts
}

type DeploymentView struct {
	domain.WorkerDeployment
	State string   `json:"state"`
	Hosts []string `json:"hosts"`
}

func (s *Service) WorkerDeployments(ctx context.Context, st *domain.State, p authorization.Principal, ref WorkerRef, options ...VersionPageOptions) (any, Outcome, error) {
	if _, err := s.WorkerCollectionTarget(st, ref, "worker.version.read", p); err != nil {
		return nil, Read, err
	}
	out := []DeploymentView{}
	for _, d := range st.Workers.Deployments {
		if d.Organization != ref.Organization || d.Worker != ref.Worker || ref.Item != "" && ref.Item != d.ID || ref.Environment != "" && ref.Environment != d.Environment {
			continue
		}
		if _, err := s.WorkerTarget(st, ref, d.Environment, "worker.version.read", p); err != nil {
			continue
		}
		worker := s.WorkerView(st, authorization.Target{ID: ref.Worker, Organization: ref.Organization})
		state := st.Workers.Operations[d.Operation].State
		if state == "" {
			state = "available"
		}
		hosts := []string{}
		if d.Committed != nil && d.Expires.After(time.Now()) {
			hosts = DeploymentHosts(s.Config.Workers, worker, d.ID)
		}
		view := DeploymentView{WorkerDeployment: d, State: state, Hosts: hosts}
		if ref.Item != "" {
			return view, Read, nil
		}
		out = append(out, view)
	}
	if ref.Item != "" {
		return nil, Read, domain.NotFound()
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Created.After(out[j].Created) || out[i].Created.Equal(out[j].Created) && out[i].ID < out[j].ID
	})
	return versionPage(out, options, func(d DeploymentView) (time.Time, string, string) {
		return d.Created, d.ID, d.ID + " " + d.Version + " " + d.State
	})
}
