package workers

import (
	"context"
	"sort"
	"time"

	"github.com/krun-dev/krun/internal/authorization"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/securetoken"
)

// A version freezes executable content and bindings without changing traffic.
type WorkerVersionUploadInput struct {
	Tag      string `json:"tag,omitempty"`
	Message  string `json:"message,omitempty"`
	Build    string `json:"build_id,omitempty"`
	Artifact string `json:"artifact_id,omitempty"`
	Key      string `json:"idempotency_key"`
}

func (s *Service) WorkerVersionUpload(ctx context.Context, st *domain.State, p authorization.Principal, ref ServiceRef, in WorkerVersionUploadInput) (any, Outcome, error) {
	target, err := s.WorkerTarget(st, ref, ref.Environment, "service.publish", p)
	if err != nil {
		return nil, Read, err
	}
	if len(in.Tag) > 100 || len(in.Message) > 1000 {
		return nil, Read, domain.Problem(422, "WORKER_VERSION_INVALID", "Version tag or message is too long")
	}
	if in.Key == "" || len(in.Key) > 128 {
		return nil, Read, domain.Problem(422, "IDEMPOTENCY_REQUIRED", "idempotency_key required")
	}
	key := WorkerIdempotencyKey(target, p, "version-upload", ref.Environment, in.Key)
	if id, err := WorkerIdempotent(st, key, in); err != nil {
		return nil, Read, err
	} else if id != "" {
		return st.Workers.Versions[id], Read, nil
	}
	service := s.WorkerService(st, target)
	environment, ok := service.Environments[ref.Environment]
	if !ok {
		return nil, Read, domain.NotFound()
	}
	if !environment.Enabled {
		return nil, Read, domain.Problem(409, "ENVIRONMENT_DISABLED", "Target environment is disabled")
	}
	artifactID := in.Artifact
	if in.Build != "" {
		if artifactID != "" {
			return nil, Read, domain.Problem(422, "WORKER_VERSION_INVALID", "Use build_id or artifact_id")
		}
		build, ok := st.Workers.Builds[in.Build]
		if !ok || build.Service != ref.Service || build.Organization != ref.Organization {
			return nil, Read, domain.NotFound()
		}
		if build.State != "succeeded" {
			return nil, Read, domain.Problem(409, "BUILD_NOT_READY", "Build must have a finalized artifact")
		}
		artifactID = build.Artifact
	}
	artifact, ok := st.Workers.Artifacts[artifactID]
	if !ok || artifact.Organization != ref.Organization || artifact.State != "finalized" {
		return nil, Read, domain.Problem(409, "ARTIFACT_UNAVAILABLE", "Artifact unavailable")
	}
	// Only a build belonging to this service or an existing version can authorize
	// reusing an artifact. An organization-wide artifact ID is not a capability.
	owned := false
	for _, b := range st.Workers.Builds {
		if b.Service == ref.Service && b.Organization == ref.Organization && b.Artifact == artifactID {
			owned = true
			break
		}
	}
	for _, v := range st.Workers.Versions {
		if v.Service == ref.Service && v.Organization == ref.Organization && v.Artifact == artifactID {
			owned = true
			break
		}
	}
	if !owned {
		return nil, Read, domain.NotFound()
	}
	version := SnapshotVersion(target.Organization, target.ID, ref.Environment, artifact, environment)
	version.Tag, version.Message = in.Tag, in.Message
	st.Workers.Versions[version.ID] = version
	WorkerRemember(st, key, in, version.ID)
	s.audit(st, p, "workers.version.create", target.ID+"/"+ref.Environment, true)
	return version, Created, nil
}
func SnapshotVersion(org, service, environment string, artifact domain.WorkerArtifact, config domain.WorkerEnvironment) domain.WorkerVersion {
	return domain.WorkerVersion{ID: securetoken.UUID(), VersionMetadata: config.VersionMetadata, AssetsBinding: config.AssetsBinding, Organization: org, Service: service, Environment: environment, Artifact: artifact.ID, Profile: artifact.Profile, ArchiveDigest: artifact.ArchiveDigest, ModuleDigest: artifact.ModuleDigest, CompatibilityDate: artifact.CompatibilityDate, CompatibilityFlags: append([]string(nil), artifact.CompatibilityFlags...), ConfigRevision: config.ConfigRevision, Variables: domain.CloneWorkerVariables(config.Variables), SecretVersions: domain.CloneStringsMap(config.SecretVersions), SecretDigest: WorkerSecretsDigest(config.SecretVersions), Created: time.Now().UTC()}
}
func (s *Service) WorkerVersionList(ctx context.Context, st *domain.State, p authorization.Principal, ref ServiceRef) (any, Outcome, error) {
	if _, err := s.WorkerCollectionTarget(st, ref, "service.version.read", p); err != nil {
		return nil, Read, err
	}
	out := []domain.WorkerVersion{}
	for _, v := range st.Workers.Versions {
		if v.Organization != ref.Organization || v.Service != ref.Service || ref.Environment != "" && v.Environment != ref.Environment {
			continue
		}
		if _, err := s.WorkerTarget(st, ref, v.Environment, "service.version.read", p); err == nil {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return collection(out)
}
