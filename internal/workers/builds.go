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
	storage "github.com/krun-dev/krun/internal/storage"
)

type WorkerBuildCreateInput struct {
	SourceType         string                     `json:"source_type"`
	RepositoryRevision *int                       `json:"repository_revision"`
	Ref                string                     `json:"ref"`
	Environment        string                     `json:"environment"`
	Entrypoint         string                     `json:"entrypoint"`
	Files              map[string]string          `json:"files"`
	SourceUpload       string                     `json:"source_upload"`
	CompatibilityDate  string                     `json:"compatibility_date"`
	CompatibilityFlags []string                   `json:"compatibility_flags"`
	Assets             *domain.WorkerAssetsConfig `json:"assets,omitempty"`
	Key                string                     `json:"idempotency_key"`
}

// JSON escaping can expand a source byte to six wire bytes (for example, a
// control character encoded as \u00xx). Keep this scoped envelope large enough
// for the full 2 MiB decoded source limit without raising every API's 1 MiB cap.
const WorkerBuildRequestBodyMax = 16 << 20

func (s *Service) WorkerBuildCreate(ctx context.Context, st *domain.State, p authorization.Principal, ref WorkerRef, in WorkerBuildCreateInput) (any, Outcome, error) {
	if in.Environment == "" {
		return nil, Read, domain.Invalid("WORKER_BUILD_INVALID", "environment is required (production or a preview slug)")
	}
	if !appconfig.WorkerEnvValid(in.Environment) || in.Key == "" || len(in.Key) > 128 {
		return nil, Read, domain.Invalid("WORKER_BUILD_INVALID", "Valid environment and idempotency_key required")
	}
	if e := workerartifact.ValidateWorkerAssets(in.Assets); e != nil {
		return nil, Read, e
	}
	t, e := s.WorkerTarget(st, ref, in.Environment, "build.start", p)
	if e != nil {
		return nil, Read, e
	}
	ws := s.Worker(st, t)
	current, exists := ws.Environments[in.Environment]
	if !exists {
		return nil, Read, domain.NotFound()
	}
	if !current.Enabled {
		return nil, Read, domain.Conflict("ENVIRONMENT_DISABLED", "Target environment is disabled")
	}
	key := WorkerIdempotencyKey(t, p, "build", in.Environment, in.Key)
	id, e := WorkerIdempotent(st, key, in)
	if e != nil {
		return nil, Read, e
	}
	if id != "" {
		return st.Workers.Builds[id], Read, nil
	}
	var repository *domain.WorkerBuildRepository
	compatibilityDate := in.CompatibilityDate
	compatibilityFlags := in.CompatibilityFlags
	switch in.SourceType {
	case "repository":
		if in.Assets != nil {
			return nil, Read, domain.Invalid("WORKER_BUILD_INVALID", "Repository assets are read from the Wrangler configuration")
		}
		if compatibilityDate == "" && len(compatibilityFlags) > 0 {
			return nil, Read, domain.Invalid("WORKER_COMPATIBILITY_INVALID", "compatibility_flags require an explicit compatibility_date for repository builds")
		}
		if compatibilityDate != "" {
			compatibilityDate, compatibilityFlags, e = workerartifact.NormalizeWorkerCompatibility(compatibilityDate, compatibilityFlags)
			if e != nil {
				return nil, Read, domain.Invalid("WORKER_COMPATIBILITY_INVALID", e.Error())
			}
		}
		if in.RepositoryRevision == nil || *in.RepositoryRevision != ws.RepositoryRevision {
			return nil, Read, domain.Conflict("REVISION_CONFLICT", "Repository configuration changed")
		}
		if ws.Repository == nil || len(in.Files) != 0 || in.Entrypoint != "" || in.SourceUpload != "" {
			return nil, Read, domain.Invalid("WORKER_REPOSITORY_INVALID", "Connect a repository before starting a repository build")
		}
		source := *ws.Repository
		if in.Ref != "" {
			source.Ref = in.Ref
		}
		provider, err := s.Repository.ValidateRepositorySource(&source, t.Organization)
		if err != nil {
			return nil, Read, err
		}
		if source.AuthorizationUser != "" {
			if err := s.Repository.CheckBindingAuthorization(ctx, provider, source.AuthorizationUser); err != nil {
				return nil, Read, err
			}
		}
		repository = &domain.WorkerBuildRepository{WorkerRepositorySource: source}
	case "", "upload":
		if in.Ref != "" {
			return nil, Read, domain.Invalid("WORKER_BUILD_INVALID", "A ref is only supported for repository builds")
		}
		if in.SourceUpload != "" {
			// Assets (up to 20 MiB) cannot travel through the JSON request body,
			// so clients upload one source bundle and reference it here.
			if !ValidWorkerSourceUpload(t.Organization, in.SourceUpload) {
				return nil, Read, domain.Invalid("WORKER_BUILD_INVALID", "source_upload must reference an uploaded source bundle")
			}
			if len(in.Files) != 0 || in.Entrypoint == "" {
				return nil, Read, domain.Invalid("WORKER_BUILD_INVALID", "source_upload requires entrypoint and no inline files")
			}
		} else {
			if in.Assets != nil {
				return nil, Read, domain.Invalid("WORKER_BUILD_INVALID", "assets require a source bundle; upload it first and pass source_upload")
			}
			if err := workerartifact.SourceValid(in.Entrypoint, in.Files); err != nil {
				return nil, Read, err
			}
		}
		compatibilityDate, compatibilityFlags, e = workerartifact.NormalizeWorkerCompatibility(compatibilityDate, compatibilityFlags)
		if e != nil {
			return nil, Read, domain.Invalid("WORKER_COMPATIBILITY_INVALID", e.Error())
		}
	default:
		return nil, Read, domain.Invalid("WORKER_BUILD_INVALID", "Use repository or upload as source_type")
	}
	count := 0
	for _, b := range st.Workers.Builds {
		if b.Worker == t.ID && b.State != "succeeded" && b.State != "failed" {
			count++
		}
	}
	if count >= storage.WorkerActiveBuildLimit {
		return nil, Read, domain.Conflict("BUILD_LIMIT", "At most 100 builds may be active per worker")
	}
	raw, _ := json.Marshal(in.Files)
	id = securetoken.NewID("bld")
	job := "krun-build-" + securetoken.Digest(id)[:24]
	if s.Config.Workers.BuilderMode == "local" {
		job = "local-" + securetoken.Digest(id)[:16]
	}
	b := domain.WorkerBuild{ID: id, Organization: t.Organization, Worker: t.ID, Environment: in.Environment, State: "queued", Entrypoint: in.Entrypoint, SourceDigest: workerartifact.BytesDigest(raw), Profile: workerartifact.WorkerProfile, CompatibilityDate: compatibilityDate, CompatibilityFlags: compatibilityFlags, Job: job, Assets: in.Assets, Created: time.Now().UTC()}
	if in.SourceUpload != "" {
		b.SourceDigest = ""
	}
	if repository != nil {
		b.Repository = repository
		b.SourceDigest = ""
	}
	st.Workers.Builds[id] = b
	st.Workers.BuildPrivate[id] = domain.WorkerBuildPrivate{Actor: WorkerActor(p), Files: in.Files, SourceKey: in.SourceUpload}
	WorkerRemember(st, key, in, id)
	return b, Queued, nil

}

func (s *Service) WorkerSourceUploadCreate(ctx context.Context, st *domain.State, p authorization.Principal, ref WorkerRef) (any, Outcome, error) {
	t, e := s.WorkerCollectionTarget(st, ref, "build.start", p)
	if e != nil {
		return nil, Read, e
	}
	key, url, e := s.Controller.SourceUploadURL(ctx, t.Organization)
	if e != nil {
		return nil, Read, domain.Unavailable("SOURCE_UPLOAD_UNAVAILABLE", "Source storage unavailable")
	}
	return map[string]any{"source_upload": key, "upload_url": url, "expires_in": 900}, Created, nil

}

func (s *Service) WorkerBuildList(ctx context.Context, st *domain.State, p authorization.Principal, ref WorkerRef) (any, Outcome, error) {
	t, e := s.WorkerCollectionTarget(st, ref, "worker.version.read", p)
	if e != nil {
		return nil, Read, e
	}
	out := []domain.WorkerBuild{}
	for _, b := range st.Workers.Builds {
		if b.Worker == t.ID {
			d, e := authorization.Authorize(st, p, "worker.version.read", authorization.Target{Kind: "worker", ID: t.ID, Organization: t.Organization, Environment: b.Environment})
			if e != nil {
				return nil, Read, e
			}
			if d.Allowed {
				out = append(out, b)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return collection(out)

}

func (s *Service) WorkerGetBuild(st *domain.State, p authorization.Principal, ref WorkerRef, action string) (domain.WorkerBuild, error) {
	b, ok := st.Workers.Builds[ref.Item]
	if !ok || b.Worker != ref.Worker || b.Organization != ref.Organization {
		return b, domain.NotFound()
	}
	_, e := s.WorkerTarget(st, ref, b.Environment, action, p)
	return b, e
}

func (s *Service) WorkerBuildGet(ctx context.Context, st *domain.State, p authorization.Principal, ref WorkerRef) (any, Outcome, error) {
	b, e := s.WorkerGetBuild(st, p, ref, "worker.version.read")
	return b, Read, e

}

type WorkerBuildLogRead struct {
	BuildID string
	State   string
	Key     string
	Expired bool
}

type WorkerBuildLogsResult struct {
	BuildID     string  `json:"build_id"`
	State       string  `json:"state"`
	DownloadURL string  `json:"download_url"`
	Expired     bool    `json:"expired"`
	Log         *string `json:"log,omitempty"`
}

func (s *Service) ResolveWorkerBuildLog(ctx context.Context, st *domain.State, p authorization.Principal, ref WorkerRef) (WorkerBuildLogRead, error) {
	b, e := s.WorkerGetBuild(st, p, ref, "log.read")
	if e != nil {
		return WorkerBuildLogRead{}, e
	}
	private, ok, e := s.Store.WorkerBuildPrivate(ctx, b.ID)
	if e != nil {
		return WorkerBuildLogRead{}, e
	}
	if !ok {
		private = domain.WorkerBuildPrivate{}
	}
	expired := time.Since(b.Created) >= 7*24*time.Hour
	if expired {
		private.LogKey = ""
	}
	return WorkerBuildLogRead{BuildID: b.ID, State: b.State, Key: private.LogKey, Expired: expired}, nil
}

// ReadWorkerBuildLog runs after the caller releases its state snapshot or transaction.
func (s *Service) ReadWorkerBuildLog(ctx context.Context, in WorkerBuildLogRead, inline bool) (WorkerBuildLogsResult, error) {
	out := WorkerBuildLogsResult{BuildID: in.BuildID, State: in.State, Expired: in.Expired}
	var err error
	if inline {
		var text string
		text, err = s.Controller.ReadBuildLog(ctx, in.Key)
		out.Log = &text
	} else {
		out.DownloadURL, err = s.Controller.BuildLogURL(ctx, in.Key)
	}
	if err != nil {
		return WorkerBuildLogsResult{}, domain.Unavailable("LOG_UNAVAILABLE", "Log storage unavailable")
	}
	return out, nil
}

func (s *Service) WorkerBuildSource(ctx context.Context, st *domain.State, p authorization.Principal, ref WorkerRef) (any, Outcome, error) {
	b, e := s.WorkerGetBuild(st, p, ref, "source.read")
	if e != nil {
		return nil, Read, e
	}
	private, ok, e := s.Store.WorkerBuildPrivate(ctx, b.ID)
	if e != nil {
		return nil, Read, e
	}
	if !ok {
		private = domain.WorkerBuildPrivate{}
	}
	if private.SourceKey != "" {
		url, e := s.Controller.SourceBundleURL(ctx, private.SourceKey)
		if e != nil {
			return nil, Read, domain.Unavailable("SOURCE_UNAVAILABLE", "Source storage unavailable")
		}
		return map[string]any{"entrypoint": b.Entrypoint, "download_url": url}, Read, nil
	}
	return map[string]any{"entrypoint": b.Entrypoint, "files": private.Files}, Read, nil

}
