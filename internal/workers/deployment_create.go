package workers

import (
	context "context"
	"encoding/json"
	"sort"
	"time"

	workerartifact "github.com/krun-dev/krun/internal/artifact"
	authorization "github.com/krun-dev/krun/internal/authorization"
	appconfig "github.com/krun-dev/krun/internal/config"
	domain "github.com/krun-dev/krun/internal/domain"
	securetoken "github.com/krun-dev/krun/internal/securetoken"
)

type WorkerDeploymentCreateInput struct {
	Build    string `json:"build_id,omitempty"`
	Version  string `json:"version_id,omitempty"`
	Revision int    `json:"expected_revision"`
	Key      string `json:"idempotency_key"`
}

func PublicWorkerVersion(version domain.WorkerVersion) domain.WorkerVersion {
	version.SecretNames = domain.WorkerSecretNames(version.SecretVersions)
	return version
}

func WorkerBusy(st *domain.State, worker, env string) bool {
	for _, op := range st.Workers.Operations {
		// A committed operation is already durable and converging; a new
		// publish or config change supersedes it instead of blocking.
		if op.Worker == worker && op.Environment == env && !appconfig.WorkerTerminal(op.State) && op.State != "committed" {
			return true
		}
	}
	return false
}

func (s *Service) WorkerDeploymentCreate(ctx context.Context, st *domain.State, p authorization.Principal, ref WorkerRef, t authorization.Target, env string, kind string, in WorkerDeploymentCreateInput) (any, Outcome, error) {
	if in.Key == "" || len(in.Key) > 128 || in.Revision < 0 {
		return nil, Read, domain.Invalid("WORKER_VERSION_INVALID", "expected_revision and idempotency_key required")
	}
	ws := s.Worker(st, t)
	key := WorkerIdempotencyKey(t, p, kind, env, in.Key)
	id, e := WorkerIdempotent(st, key, in)
	if e != nil {
		return nil, Read, e
	}
	if id != "" {
		return st.Workers.Operations[id], Read, nil
	}
	current, exists := ws.Environments[env]
	if !exists {
		return nil, Read, domain.NotFound()
	}
	if !current.Enabled {
		return nil, Read, domain.Conflict("ENVIRONMENT_DISABLED", "Target environment is disabled")
	}
	if current.Revision != in.Revision {
		return nil, Read, domain.Conflict("REVISION_CONFLICT", "Environment version changed")
	}
	if WorkerBusy(st, t.ID, env) {
		return nil, Read, domain.Conflict("DEPLOYMENT_BUSY", "Wait for the active version operation")
	}
	var version domain.WorkerVersion
	if kind == "publish" && in.Version != "" && in.Build == "" {
		var exists bool
		version, exists = st.Workers.Versions[in.Version]
		if !exists || version.Worker != t.ID || version.Organization != t.Organization || version.Environment != env {
			return nil, Read, domain.NotFound()
		}
	} else if kind == "publish" {
		if in.Build == "" || in.Version != "" {
			return nil, Read, domain.Invalid("WORKER_VERSION_INVALID", "Publish requires build_id only")
		}
		b, ok := st.Workers.Builds[in.Build]
		if !ok || b.Worker != t.ID || b.Organization != t.Organization {
			return nil, Read, domain.NotFound()
		}
		if b.State != "succeeded" {
			return nil, Read, domain.Conflict("BUILD_NOT_READY", "Build must have a finalized artifact")
		}
		a, ok := st.Workers.Artifacts[b.Artifact]
		if !ok || a.State != "finalized" {
			return nil, Read, domain.Conflict("ARTIFACT_UNAVAILABLE", "Artifact unavailable")
		}
		version = versionSnapshot(t.Organization, t.ID, env, a, current)
		version.Configuration = inheritedConfiguration(st, current)
		raw, _ := json.Marshal(version)
		digest := workerartifact.BytesDigest(raw)
		version.ID = "rel-" + digest
		version.Created = time.Now().UTC()
		version.CreatedBy = p.User.ID
		version.CreatedByName = p.User.Name
		if old, ok := st.Workers.Versions[version.ID]; ok {
			version = old
		} else if old, ok := st.Workers.Versions["rel_"+digest]; ok {
			version = old
		}
		st.Workers.Versions[version.ID] = version
	} else {
		if in.Version == "" || in.Build != "" {
			return nil, Read, domain.Invalid("WORKER_VERSION_INVALID", "Rollback requires version_id only")
		}
		d, ok := st.Workers.Versions[in.Version]
		if !ok || d.Worker != t.ID || d.Organization != t.Organization || d.Environment != env {
			return nil, Read, domain.NotFound()
		}
		historical := false
		for _, h := range st.Workers.Histories[t.ID+"/"+env] {
			if h.Version == d.ID {
				historical = true
			}
		}
		if !historical {
			return nil, Read, domain.Conflict("ROLLBACK_NOT_PUBLISHED", "Rollback target must be a successfully published version")
		}
		version = d
	}
	return queueWorkerDeployment(st, p, domain.WorkerOperation{
		Kind:             kind,
		Organization:     t.Organization,
		Worker:           t.ID,
		Environment:      env,
		Build:            in.Build,
		Version:          version.ID,
		ExpectedRevision: current.Revision,
		ConfigRevision:   current.ConfigRevision,
	}, key, in), Queued, nil
}

func queueWorkerDeployment(st *domain.State, p authorization.Principal, op domain.WorkerOperation, key string, input any) domain.WorkerOperation {
	op.ID = securetoken.NewID("op")
	op.State = "queued"
	op.Steps = []domain.WorkerStep{
		{Name: "artifact", State: "pending"},
		{Name: "candidate", State: "pending"},
		{Name: "commit", State: "pending"},
		{Name: "synchronize", State: "pending"},
	}
	op.Created = time.Now().UTC()
	st.Workers.Operations[op.ID] = op
	RememberDeployment(st, op)
	st.Workers.OperationPrivate[op.ID] = domain.WorkerOperationPrivate{Actor: WorkerActor(p)}
	WorkerRemember(st, key, input, op.ID)
	return op
}

func (s *Service) WorkerOperationGet(ctx context.Context, st *domain.State, p authorization.Principal, ref WorkerRef) (any, Outcome, error) {
	op, ok := st.Workers.Operations[ref.Item]
	if !ok || op.Worker != ref.Worker || op.Organization != ref.Organization {
		return nil, Read, domain.NotFound()
	}
	_, e := s.WorkerTarget(st, ref, op.Environment, "worker.read", p)
	return op, Read, e

}

func (s *Service) WorkerVersionHistory(ctx context.Context, st *domain.State, p authorization.Principal, ref WorkerRef) (any, Outcome, error) {
	env := ref.Environment
	if !appconfig.WorkerEnvValid(env) {
		return nil, Read, domain.NotFound()
	}
	t, e := s.WorkerTarget(st, ref, env, "worker.version.read", p)
	if e != nil {
		return nil, Read, e
	}
	out := st.Workers.Histories[t.ID+"/"+env]
	if out == nil {
		out = []domain.WorkerHistory{}
	}
	return collection(out)

}

func (s *Service) WorkerOperationList(ctx context.Context, st *domain.State, p authorization.Principal, ref WorkerRef) (any, Outcome, error) {
	t, e := s.WorkerCollectionTarget(st, ref, "worker.version.read", p)
	if e != nil {
		return nil, Read, e
	}
	out := []domain.WorkerOperation{}
	for _, op := range st.Workers.Operations {
		if op.Worker != t.ID || op.Organization != t.Organization {
			continue
		}
		target := t
		target.Environment = op.Environment
		d, e := authorization.Authorize(st, p, "worker.version.read", target)
		if e != nil {
			return nil, Read, e
		}
		if d.Allowed {
			out = append(out, op)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	if len(out) > 100 {
		out = out[:100]
	}
	return collection(out)

}

func (s *Service) WorkerVersionGet(ctx context.Context, st *domain.State, p authorization.Principal, ref WorkerRef) (any, Outcome, error) {
	d, ok := st.Workers.Versions[ref.Item]
	if !ok || d.Worker != ref.Worker || d.Organization != ref.Organization {
		return nil, Read, domain.NotFound()
	}
	_, e := s.WorkerTarget(st, ref, d.Environment, "worker.version.read", p)
	return PublicWorkerVersion(d), Read, e

}
