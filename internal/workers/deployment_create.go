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
	return version
}

func WorkerBusy(st *domain.State, service, env string) bool {
	for _, op := range st.Workers.Operations {
		// A committed operation is already durable and converging; a new
		// publish or config change supersedes it instead of blocking.
		if op.Service == service && op.Environment == env && !appconfig.WorkerTerminal(op.State) && op.State != "committed" {
			return true
		}
	}
	return false
}

func (s *Service) WorkerDeploymentCreate(ctx context.Context, st *domain.State, p authorization.Principal, ref ServiceRef, t authorization.Target, env string, kind string, in WorkerDeploymentCreateInput) (any, Outcome, error) {
	if in.Key == "" || len(in.Key) > 128 || in.Revision < 0 {
		return nil, Read, domain.Problem(422, "WORKER_VERSION_INVALID", "expected_revision and idempotency_key required")
	}
	ws := s.WorkerService(st, t)
	current := ws.Environments[env]
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
		return nil, Read, domain.Problem(409, "ENVIRONMENT_DISABLED", "Target environment is disabled")
	}
	if current.Revision != in.Revision {
		return nil, Read, domain.Problem(409, "REVISION_CONFLICT", "Environment version changed")
	}
	for _, active := range st.Workers.Operations {
		// Cancellation is only possible before commit; a committed version is
		// durable and may be replaced by a newer publish.
		if active.Service == t.ID && active.Environment == env && !appconfig.WorkerTerminal(active.State) && active.State != "committed" {
			return nil, Read, domain.Problem(409, "DEPLOYMENT_BUSY", "Wait for the active version operation")
		}
	}
	var version domain.WorkerVersion
	if kind == "publish" && in.Version != "" && in.Build == "" {
		var exists bool
		version, exists = st.Workers.Versions[in.Version]
		if !exists || version.Service != t.ID || version.Organization != t.Organization || version.Environment != env {
			return nil, Read, domain.NotFound()
		}
	} else if kind == "publish" {
		if in.Build == "" || in.Version != "" {
			return nil, Read, domain.Problem(422, "WORKER_VERSION_INVALID", "Publish requires build_id only")
		}
		b, ok := st.Workers.Builds[in.Build]
		if !ok || b.Service != t.ID || b.Organization != t.Organization {
			return nil, Read, domain.NotFound()
		}
		if b.State != "succeeded" {
			return nil, Read, domain.Problem(409, "BUILD_NOT_READY", "Build must have a finalized artifact")
		}
		a, ok := st.Workers.Artifacts[b.Artifact]
		if !ok || a.State != "finalized" {
			return nil, Read, domain.Problem(409, "ARTIFACT_UNAVAILABLE", "Artifact unavailable")
		}
		version = domain.WorkerVersion{
			R2Bindings:         domain.CloneR2Bindings(current.R2Bindings),
			VersionMetadata:    current.VersionMetadata,
			AssetsBinding:      current.AssetsBinding,
			Service:            t.ID,
			Organization:       t.Organization,
			Environment:        env,
			Artifact:           a.ID,
			Profile:            a.Profile,
			ArchiveDigest:      a.ArchiveDigest,
			ModuleDigest:       a.ModuleDigest,
			CompatibilityDate:  a.CompatibilityDate,
			CompatibilityFlags: append([]string(nil), a.CompatibilityFlags...),
			ConfigRevision:     current.ConfigRevision,
			Variables:          domain.CloneWorkerVariables(current.Variables),
			SecretDigest:       WorkerSecretsDigest(current.SecretVersions),
			SecretVersions:     domain.CloneStringsMap(current.SecretVersions),
		}
		raw, _ := json.Marshal(version)
		version.ID = "rel_" + workerartifact.BytesDigest(raw)
		version.Created = time.Now().UTC()
		if old, ok := st.Workers.Versions[version.ID]; ok {
			version = old
		}
		st.Workers.Versions[version.ID] = version
	} else {
		if in.Version == "" || in.Build != "" {
			return nil, Read, domain.Problem(422, "WORKER_VERSION_INVALID", "Rollback requires version_id only")
		}
		d, ok := st.Workers.Versions[in.Version]
		if !ok || d.Service != t.ID || d.Organization != t.Organization || d.Environment != env {
			return nil, Read, domain.NotFound()
		}
		historical := false
		for _, h := range st.Workers.Histories[t.ID+"/"+env] {
			if h.Version == d.ID {
				historical = true
			}
		}
		if !historical {
			return nil, Read, domain.Problem(409, "ROLLBACK_NOT_PUBLISHED", "Rollback target must be a successfully published version")
		}
		version = d
	}
	id = securetoken.NewID("op")
	op := domain.WorkerOperation{
		ID:               id,
		Kind:             kind,
		Organization:     t.Organization,
		Service:          t.ID,
		Environment:      env,
		State:            "queued",
		Build:            in.Build,
		Version:          version.ID,
		ExpectedRevision: current.Revision,
		ConfigRevision:   current.ConfigRevision,
		Steps:            []domain.WorkerStep{{Name: "artifact", State: "pending"}, {Name: "candidate", State: "pending"}, {Name: "commit", State: "pending"}, {Name: "synchronize", State: "pending"}},
		Created:          time.Now().UTC(),
	}
	st.Workers.Operations[id] = op
	RememberDeployment(st, op)
	st.Workers.OperationPrivate[id] = domain.WorkerOperationPrivate{Actor: WorkerActor(p)}
	WorkerRemember(st, key, in, id)
	return op, Queued, nil

}

func (s *Service) WorkerOperationGet(ctx context.Context, st *domain.State, p authorization.Principal, ref ServiceRef) (any, Outcome, error) {
	op, ok := st.Workers.Operations[ref.Item]
	if !ok || op.Service != ref.Service || op.Organization != ref.Organization {
		return nil, Read, domain.NotFound()
	}
	_, e := s.WorkerTarget(st, ref, op.Environment, "service.read", p)
	return op, Read, e

}

func (s *Service) WorkerVersionHistory(ctx context.Context, st *domain.State, p authorization.Principal, ref ServiceRef) (any, Outcome, error) {
	env := ref.Environment
	if !appconfig.WorkerEnvValid(env) {
		return nil, Read, domain.NotFound()
	}
	t, e := s.WorkerTarget(st, ref, env, "service.version.read", p)
	if e != nil {
		return nil, Read, e
	}
	out := st.Workers.Histories[t.ID+"/"+env]
	if out == nil {
		out = []domain.WorkerHistory{}
	}
	return collection(out)

}

func (s *Service) WorkerOperationList(ctx context.Context, st *domain.State, p authorization.Principal, ref ServiceRef) (any, Outcome, error) {
	t, e := s.WorkerCollectionTarget(st, ref, "service.version.read", p)
	if e != nil {
		return nil, Read, e
	}
	out := []domain.WorkerOperation{}
	for _, op := range st.Workers.Operations {
		if op.Service != t.ID || op.Organization != t.Organization {
			continue
		}
		target := t
		target.Environment = op.Environment
		d, e := authorization.Authorize(st, p, "service.version.read", target)
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

func (s *Service) WorkerVersionGet(ctx context.Context, st *domain.State, p authorization.Principal, ref ServiceRef) (any, Outcome, error) {
	d, ok := st.Workers.Versions[ref.Item]
	if !ok || d.Service != ref.Service || d.Organization != ref.Organization {
		return nil, Read, domain.NotFound()
	}
	_, e := s.WorkerTarget(st, ref, d.Environment, "service.version.read", p)
	return PublicWorkerVersion(d), Read, e

}
