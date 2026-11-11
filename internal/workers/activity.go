package workers

import (
	"encoding/json"
	"sort"

	"github.com/krun-dev/krun/internal/authorization"
	appconfig "github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/securetoken"
)

type WorkerActivity struct {
	Active bool   `json:"active"`
	Token  string `json:"token"`
}

func (s *Service) WorkerActivity(st *domain.State, p authorization.Principal, ref WorkerRef) (WorkerActivity, error) {
	target, err := s.WorkerCollectionTarget(st, ref, "worker.read", p)
	if err != nil {
		return WorkerActivity{}, err
	}
	envs := st.Workers.Workers[target.ID].Environments
	names := []string{appconfig.WorkerBaseline}
	for name := range envs {
		if name != appconfig.WorkerBaseline {
			names = append(names, name)
		}
	}
	visible := make(map[string]bool, len(names))
	for _, name := range names {
		target.Environment = name
		read, versions, err := workerEnvironmentReadAccess(st, p, target)
		if err != nil {
			return WorkerActivity{}, err
		}
		if read {
			visible[name] = versions
		}
	}

	builds := make([]domain.WorkerBuild, 0, len(st.Workers.Builds))
	for _, build := range st.Workers.Builds {
		if build.Worker == target.ID && build.Organization == target.Organization && visible[build.Environment] {
			builds = append(builds, build)
		}
	}
	operations := make([]domain.WorkerOperation, 0, len(st.Workers.Operations))
	for _, op := range st.Workers.Operations {
		if op.Worker == target.ID && op.Organization == target.Organization && visible[op.Environment] {
			operations = append(operations, op)
		}
	}
	return summarizeWorkerActivity(visible, builds, operations)
}

func workerEnvironmentReadAccess(st *domain.State, p authorization.Principal, target authorization.Target) (read, versions bool, err error) {
	decision, err := authorization.Authorize(st, p, "worker.read", target)
	if err != nil || !decision.Allowed {
		return false, false, err
	}
	versionDecision, err := authorization.Authorize(st, p, "worker.version.read", target)
	return true, versionDecision.Allowed, err
}

func summarizeWorkerActivity(visible map[string]bool, builds []domain.WorkerBuild, operations []domain.WorkerOperation) (WorkerActivity, error) {
	// Use the same visible, unfinished rows for both API reads so a
	// progress update or permission change invalidates the overview.
	state := struct {
		Environments map[string]bool
		Builds       []domain.WorkerBuild
		Operations   []domain.WorkerOperation
	}{Environments: visible}
	for _, build := range builds {
		if !appconfig.WorkerTerminal(build.State) {
			state.Builds = append(state.Builds, build)
		}
	}
	for _, op := range operations {
		if !appconfig.WorkerTerminal(op.State) {
			state.Operations = append(state.Operations, op)
		}
	}
	sort.Slice(state.Builds, func(i, j int) bool { return state.Builds[i].ID < state.Builds[j].ID })
	sort.Slice(state.Operations, func(i, j int) bool { return state.Operations[i].ID < state.Operations[j].ID })
	encoded, err := json.Marshal(state)
	if err != nil {
		return WorkerActivity{}, err
	}
	return WorkerActivity{
		Active: len(state.Builds) > 0 || len(state.Operations) > 0,
		Token:  securetoken.DigestBytes(encoded),
	}, nil
}
