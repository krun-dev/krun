package workers

import (
	"context"
	"maps"
	"sort"
	"time"

	"github.com/krun-dev/krun/internal/authorization"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/securetoken"
)

// A version freezes executable content and bindings without changing traffic.
type WorkerVersionUploadInput struct {
	Tag                   string `json:"tag,omitempty"`
	Message               string `json:"message,omitempty"`
	Build                 string `json:"build_id,omitempty"`
	Artifact              string `json:"artifact_id,omitempty"`
	ConfigurationRevision *int   `json:"expected_configuration_revision,omitempty"`
	Key                   string `json:"idempotency_key"`
}

func (s *Service) WorkerVersionUpload(ctx context.Context, st *domain.State, p authorization.Principal, ref WorkerRef, in WorkerVersionUploadInput) (any, Outcome, error) {
	target, err := s.WorkerTarget(st, ref, ref.Environment, "worker.publish", p)
	if err != nil {
		return nil, Read, err
	}
	if len(in.Tag) > 100 || len(in.Message) > 1000 {
		return nil, Read, domain.Invalid("WORKER_VERSION_INVALID", "Version tag or message is too long")
	}
	if in.Key == "" || len(in.Key) > 128 {
		return nil, Read, domain.Invalid("IDEMPOTENCY_REQUIRED", "idempotency_key required")
	}
	key := WorkerIdempotencyKey(target, p, "version-upload", ref.Environment, in.Key)
	if id, err := WorkerIdempotent(st, key, in); err != nil {
		return nil, Read, err
	} else if id != "" {
		return st.Workers.Versions[id], Read, nil
	}
	worker := s.Worker(st, target)
	environment, ok := worker.Environments[ref.Environment]
	if !ok {
		return nil, Read, domain.NotFound()
	}
	if !environment.Enabled {
		return nil, Read, domain.Conflict("ENVIRONMENT_DISABLED", "Target environment is disabled")
	}
	if in.ConfigurationRevision != nil && *in.ConfigurationRevision != environment.ConfigRevision {
		return nil, Read, domain.Conflict("REVISION_CONFLICT", "Environment configuration changed")
	}
	artifactID := in.Artifact
	if in.Build != "" {
		if artifactID != "" {
			return nil, Read, domain.Invalid("WORKER_VERSION_INVALID", "Use build_id or artifact_id")
		}
		build, ok := st.Workers.Builds[in.Build]
		if !ok || build.Worker != ref.Worker || build.Organization != ref.Organization {
			return nil, Read, domain.NotFound()
		}
		if build.State != "succeeded" {
			return nil, Read, domain.Conflict("BUILD_NOT_READY", "Build must have a finalized artifact")
		}
		artifactID = build.Artifact
	}
	artifact, ok := st.Workers.Artifacts[artifactID]
	if !ok || artifact.Organization != ref.Organization || artifact.State != "finalized" {
		return nil, Read, domain.Conflict("ARTIFACT_UNAVAILABLE", "Artifact unavailable")
	}
	// Only a build belonging to this worker or an existing version can authorize
	// reusing an artifact. An organization-wide artifact ID is not a capability.
	owned := false
	for _, b := range st.Workers.Builds {
		if b.Worker == ref.Worker && b.Organization == ref.Organization && b.Artifact == artifactID {
			owned = true
			break
		}
	}
	for _, v := range st.Workers.Versions {
		if v.Worker == ref.Worker && v.Organization == ref.Organization && v.Artifact == artifactID {
			owned = true
			break
		}
	}
	if !owned {
		return nil, Read, domain.NotFound()
	}
	version := SnapshotVersion(target.Organization, target.ID, ref.Environment, artifact, environment)
	version.Tag, version.Message = in.Tag, in.Message
	version.CreatedBy = p.User.ID
	version.CreatedByName = p.User.Name
	version.Configuration = inheritedConfiguration(st, environment)
	st.Workers.Versions[version.ID] = version
	WorkerRemember(st, key, in, version.ID)
	s.audit(st, p, "workers.version.create", target.ID+"/"+ref.Environment, true)
	return version, Created, nil
}
func SnapshotVersion(org, worker, environment string, artifact domain.WorkerArtifact, config domain.WorkerEnvironment) domain.WorkerVersion {
	version := versionSnapshot(org, worker, environment, artifact, config)
	version.ID = securetoken.UUID()
	version.Created = time.Now().UTC()
	return version
}

func versionSnapshot(org, worker, environment string, artifact domain.WorkerArtifact, config domain.WorkerEnvironment) domain.WorkerVersion {
	return domain.WorkerVersion{
		R2Bindings:         domain.CloneR2Bindings(config.R2Bindings),
		HyperdriveBindings: cloneHyperdriveBindings(config.HyperdriveBindings),
		ServiceBindings:    maps.Clone(config.ServiceBindings),
		VersionMetadata:    config.VersionMetadata,
		AssetsBinding:      config.AssetsBinding,
		Organization:       org,
		Worker:             worker,
		Environment:        environment,
		Artifact:           artifact.ID,
		Profile:            artifact.Profile,
		ArchiveDigest:      artifact.ArchiveDigest,
		ModuleDigest:       artifact.ModuleDigest,
		CompatibilityDate:  artifact.CompatibilityDate,
		CompatibilityFlags: append([]string(nil), artifact.CompatibilityFlags...),
		ConfigRevision:     config.ConfigRevision,
		Variables:          domain.CloneWorkerVariables(config.Variables),
		SecretVersions:     domain.CloneStringsMap(config.SecretVersions),
		SecretDigest:       WorkerSecretsDigest(config.SecretVersions),
	}
}
func (s *Service) WorkerVersionList(ctx context.Context, st *domain.State, p authorization.Principal, ref WorkerRef, options ...VersionPageOptions) (any, Outcome, error) {
	if _, err := s.WorkerCollectionTarget(st, ref, "worker.version.read", p); err != nil {
		return nil, Read, err
	}
	out := []domain.WorkerVersion{}
	for _, v := range st.Workers.Versions {
		if v.Organization != ref.Organization || v.Worker != ref.Worker || ref.Environment != "" && v.Environment != ref.Environment {
			continue
		}
		if _, err := s.WorkerTarget(st, ref, v.Environment, "worker.version.read", p); err == nil {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Created.After(out[j].Created) || out[i].Created.Equal(out[j].Created) && out[i].ID < out[j].ID
	})
	return versionPage(out, options, func(v domain.WorkerVersion) (time.Time, string, string) {
		return v.Created, v.ID, v.ID + " " + v.Tag + " " + v.Message + " " + v.CreatedBy + " " + v.CreatedByName
	})
}
