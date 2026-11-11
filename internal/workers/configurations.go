package workers

import (
	"context"
	"maps"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/krun-dev/krun/internal/authorization"
	appconfig "github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/securetoken"
)

type WorkerConfigurationCreateInput struct {
	Name            string             `json:"name"`
	Message         string             `json:"message,omitempty"`
	Source          string             `json:"source_configuration_id,omitempty"`
	Version         string             `json:"source_version_id,omitempty"`
	Revision        int                `json:"expected_configuration_revision"`
	Variables       map[string]any     `json:"variables,omitempty"`
	R2Buckets       *map[string]string `json:"r2_buckets,omitempty"`
	VersionMetadata *string            `json:"version_metadata,omitempty"`
	Secrets         map[string]*string `json:"secrets,omitempty"`
	Key             string             `json:"idempotency_key"`
}

type WorkerConfigurationDeployInput struct {
	Build          string `json:"build_id,omitempty"`
	CodeVersion    string `json:"code_version_id,omitempty"`
	Revision       int    `json:"expected_revision"`
	ConfigRevision int    `json:"expected_configuration_revision"`
	Key            string `json:"idempotency_key"`
}

func configurationEnvironment(c domain.WorkerConfiguration) domain.WorkerEnvironment {
	return domain.WorkerEnvironment{
		Name: c.Environment, Enabled: true,
		Variables:          domain.CloneWorkerVariables(c.Variables),
		R2Bindings:         domain.CloneR2Bindings(c.R2Bindings),
		HyperdriveBindings: cloneHyperdriveBindings(c.HyperdriveBindings),
		VersionMetadata:    c.VersionMetadata, AssetsBinding: c.AssetsBinding,
		SecretVersions: domain.CloneStringsMap(c.SecretVersions),
		SecretNames:    domain.WorkerSecretNames(c.SecretVersions),
	}
}

func (s *Service) configurationTarget(st *domain.State, p authorization.Principal, ref WorkerRef, action string) (authorization.Target, error) {
	if !appconfig.WorkerEnvValid(ref.Environment) {
		return authorization.Target{}, domain.NotFound()
	}
	target, err := s.WorkerTarget(st, ref, ref.Environment, action, p)
	if err != nil {
		return target, err
	}
	if _, exists := s.WorkerView(st, target).Environments[ref.Environment]; !exists {
		return target, domain.NotFound()
	}
	return target, nil
}

func (s *Service) WorkerConfigurationList(ctx context.Context, st *domain.State, p authorization.Principal, ref WorkerRef) (any, Outcome, error) {
	if _, err := s.configurationTarget(st, p, ref, "worker.version.read"); err != nil {
		return nil, Read, err
	}
	items := []domain.WorkerConfiguration{}
	for _, c := range st.Workers.Configurations {
		if c.Organization == ref.Organization && c.Worker == ref.Worker && c.Environment == ref.Environment {
			items = append(items, c)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Created.Equal(items[j].Created) {
			return items[i].ID > items[j].ID
		}
		return items[i].Created.After(items[j].Created)
	})
	return collection(items)
}

func (s *Service) WorkerConfigurationCreate(ctx context.Context, st *domain.State, p authorization.Principal, ref WorkerRef, in WorkerConfigurationCreateInput) (any, Outcome, error) {
	target, err := s.configurationTarget(st, p, ref, "worker.update")
	if err != nil {
		return nil, Read, err
	}
	name := strings.TrimSpace(in.Name)
	if name == "" || !utf8.ValidString(name) || len(name) > 100 || len(in.Message) > 1000 || in.Source != "" && in.Version != "" {
		return nil, Read, domain.Invalid("WORKER_CONFIGURATION_INVALID", "Configuration name is required (100 bytes maximum); message allows 1000 bytes")
	}
	if in.Key == "" || len(in.Key) > 128 {
		return nil, Read, domain.Invalid("IDEMPOTENCY_REQUIRED", "idempotency_key required")
	}
	key := WorkerIdempotencyKey(target, p, "configuration-create", ref.Environment, in.Key)
	if id, err := WorkerIdempotent(st, key, in); err != nil {
		return nil, Read, err
	} else if id != "" {
		c, exists := st.Workers.Configurations[id]
		if !exists {
			return nil, Read, domain.NotFound()
		}
		return c, Read, nil
	}
	current := s.Worker(st, target).Environments[ref.Environment]
	if current.ConfigRevision != in.Revision {
		return nil, Read, domain.Conflict("REVISION_CONFLICT", "Environment configuration changed")
	}
	config := current
	if in.Source != "" {
		source, exists := st.Workers.Configurations[in.Source]
		if !exists || source.Worker != ref.Worker || source.Organization != ref.Organization || source.Environment != ref.Environment {
			return nil, Read, domain.NotFound()
		}
		if err := s.require(st, p, "worker.version.read", target); err != nil {
			return nil, Read, err
		}
		config = configurationEnvironment(source)
	}
	if in.Version != "" {
		version, exists := st.Workers.Versions[in.Version]
		if !exists || version.Worker != ref.Worker || version.Organization != ref.Organization || version.Environment != ref.Environment {
			return nil, Read, domain.NotFound()
		}
		if err := s.require(st, p, "worker.version.read", target); err != nil {
			return nil, Read, err
		}
		config = domain.WorkerEnvironment{
			Variables: version.Variables, R2Bindings: version.R2Bindings,
			HyperdriveBindings: cloneHyperdriveBindings(version.HyperdriveBindings),
			SecretVersions:     version.SecretVersions, VersionMetadata: version.VersionMetadata,
			AssetsBinding: version.AssetsBinding,
		}
	}
	if in.Variables != nil {
		config.Variables = in.Variables
	}
	if !appconfig.WorkerVariablesValid(config.Variables) {
		return nil, Read, domain.Invalid("WORKER_VARIABLES_INVALID", "Invalid variable names or size")
	}
	if in.VersionMetadata != nil {
		config.VersionMetadata = *in.VersionMetadata
	}
	if in.R2Buckets != nil {
		if len(*in.R2Buckets) > 0 && (s.Config.Workers.ObjectStorage == nil || s.Config.Workers.ObjectStorage.CursorKey == "") {
			return nil, Read, domain.Unavailable("OBJECT_STORAGE_NOT_CONFIGURED", "R2 storage must be configured")
		}
		config.R2Bindings, err = resolveR2Bindings(st, ref.Organization, *in.R2Buckets)
		if err != nil {
			return nil, Read, err
		}
	}

	if len(in.Secrets) > WorkerSecretMaxCount {
		return nil, Read, domain.Invalid("WORKER_SECRET_INVALID", "At most 100 secrets per environment")
	}
	if len(in.Secrets) > 0 {
		if err := s.require(st, p, "worker.publish", target); err != nil {
			return nil, Read, err
		}
		config.SecretVersions = domain.CloneStringsMap(config.SecretVersions)
		for name, value := range in.Secrets {
			if !WorkerSecretNameValid(name) || strings.HasPrefix(name, "KRUN_") || value != nil && len(*value) > WorkerSecretMaxBytes {
				return nil, Read, domain.Invalid("WORKER_SECRET_INVALID", "Invalid secret name or size")
			}
			if value == nil {
				delete(config.SecretVersions, name)
				continue
			}
			encrypted, err := EncryptWorkerSecret(s.Config.Workers, ref.Organization, ref.Worker, ref.Environment, name, p.User.ID, *value)
			if err != nil {
				return nil, Read, domain.Unavailable("WORKER_SECRETS_NOT_CONFIGURED", "Worker secret encryption is not configured")
			}
			st.Workers.SecretVersions[encrypted.Version.ID] = encrypted.Version
			if st.PendingWorkerSecrets == nil {
				st.PendingWorkerSecrets = map[string]domain.EncryptedWorkerSecret{}
			}
			st.PendingWorkerSecrets[encrypted.Version.ID] = encrypted
			config.SecretVersions[name] = encrypted.Version.ID
		}
	}
	if len(config.SecretVersions) > WorkerSecretMaxCount {
		return nil, Read, domain.Invalid("WORKER_SECRET_INVALID", "At most 100 secrets per environment")
	}
	if err := ValidateWorkerBindings(config); err != nil {
		return nil, Read, err
	}
	c := domain.WorkerConfiguration{
		ID: securetoken.NewID("cfg"), Organization: ref.Organization, Worker: ref.Worker,
		Environment: ref.Environment, Name: name, Message: in.Message,
		Variables: domain.CloneWorkerVariables(config.Variables), R2Bindings: domain.CloneR2Bindings(config.R2Bindings),
		HyperdriveBindings: cloneHyperdriveBindings(config.HyperdriveBindings),
		VersionMetadata:    config.VersionMetadata, AssetsBinding: config.AssetsBinding,
		SecretVersions: domain.CloneStringsMap(config.SecretVersions), SecretNames: domain.WorkerSecretNames(config.SecretVersions),
		Created: time.Now().UTC(),
	}
	st.Workers.Configurations[c.ID] = c
	if st.PendingWorkerConfigurationGuards == nil {
		st.PendingWorkerConfigurationGuards = map[string]int{}
	}
	st.PendingWorkerConfigurationGuards[c.ID] = current.ConfigRevision
	WorkerRemember(st, key, in, c.ID)
	s.audit(st, p, "workers.configuration.create", ref.Worker+"/"+ref.Environment+"/"+c.ID, true)
	return c, Created, nil
}

func (s *Service) WorkerConfigurationDeploy(ctx context.Context, st *domain.State, p authorization.Principal, ref WorkerRef, in WorkerConfigurationDeployInput) (any, Outcome, error) {
	target, err := s.configurationTarget(st, p, ref, "worker.publish")
	if err != nil {
		return nil, Read, err
	}
	if in.Key == "" || len(in.Key) > 128 {
		return nil, Read, domain.Invalid("IDEMPOTENCY_REQUIRED", "idempotency_key required")
	}
	// Include the selected configuration in the idempotency digest.
	request := struct {
		WorkerConfigurationDeployInput
		Configuration string
	}{in, ref.Item}
	key := WorkerIdempotencyKey(target, p, "configuration-publish", ref.Environment, in.Key)
	if id, err := WorkerIdempotent(st, key, request); err != nil {
		return nil, Read, err
	} else if id != "" {
		return st.Workers.Operations[id], Read, nil
	}
	c, exists := st.Workers.Configurations[ref.Item]
	if !exists || c.Worker != ref.Worker || c.Organization != ref.Organization || c.Environment != ref.Environment {
		return nil, Read, domain.NotFound()
	}
	if err := s.require(st, p, "worker.version.read", target); err != nil {
		return nil, Read, err
	}
	worker := s.Worker(st, target)
	current := worker.Environments[ref.Environment]
	if current.Revision != in.Revision || current.ConfigRevision != in.ConfigRevision {
		return nil, Read, domain.Conflict("REVISION_CONFLICT", "Environment configuration or deployment changed")
	}
	if (in.CodeVersion == "") == (in.Build == "") {
		return nil, Read, domain.Invalid("WORKER_CONFIGURATION_INVALID", "Select code_version_id or build_id")
	}
	artifactID := ""
	if in.CodeVersion != "" {
		version, ok := st.Workers.Versions[in.CodeVersion]
		if !ok || version.Organization != ref.Organization || version.Worker != ref.Worker || version.Environment != ref.Environment || current.Version != version.ID {
			return nil, Read, domain.Conflict("REVISION_CONFLICT", "Current code version changed")
		}
		if current.Confirmed != "" && current.Confirmed != current.Version {
			return nil, Read, domain.Conflict("DEPLOYMENT_BUSY", "Wait for the current deployment to synchronize")
		}
		artifactID = version.Artifact
	} else {
		build, ok := st.Workers.Builds[in.Build]
		if !ok || build.Organization != ref.Organization || build.Worker != ref.Worker {
			return nil, Read, domain.NotFound()
		}
		if build.State != "succeeded" {
			return nil, Read, domain.Conflict("BUILD_NOT_READY", "Build must have a finalized artifact")
		}
		artifactID = build.Artifact
	}
	artifact, ok := st.Workers.Artifacts[artifactID]
	if !ok || artifact.State != "finalized" || artifact.Organization != ref.Organization {
		return nil, Read, domain.Conflict("ARTIFACT_UNAVAILABLE", "Artifact unavailable")
	}
	config := configurationEnvironment(c)
	for _, binding := range config.R2Bindings {
		bucket, ok := st.Workers.Buckets[binding.BucketID]
		if !ok || !bucket.Enabled || bucket.Organization != ref.Organization {
			return nil, Read, domain.Conflict("STORAGE_BUCKET_UNAVAILABLE", "Bucket is unavailable")
		}
	}
	if err := ValidateWorkerBindings(config); err != nil {
		return nil, Read, err
	}
	current.Variables = config.Variables
	current.R2Bindings = config.R2Bindings
	current.HyperdriveBindings = config.HyperdriveBindings
	current.SecretVersions = config.SecretVersions
	current.SecretNames = config.SecretNames
	current.VersionMetadata = config.VersionMetadata
	current.AssetsBinding = config.AssetsBinding
	current.ConfigRevision++
	worker.Environments[ref.Environment] = current
	st.Workers.Workers[ref.Worker] = worker
	version := SnapshotVersion(ref.Organization, ref.Worker, ref.Environment, artifact, current)
	version.Configuration = c.ID
	version.CreatedBy = p.User.ID
	version.CreatedByName = p.User.Name
	version.Tag, version.Message = c.Name, c.Message
	st.Workers.Versions[version.ID] = version
	out, outcome, err := s.WorkerDeploymentCreate(ctx, st, p, ref, target, ref.Environment, "publish", WorkerDeploymentCreateInput{Version: version.ID, Revision: in.Revision, Key: in.Key})
	if err != nil {
		return nil, Read, err
	}
	op := out.(domain.WorkerOperation)
	WorkerRemember(st, key, request, op.ID)
	s.audit(st, p, "workers.configuration.publish", ref.Worker+"/"+ref.Environment+"/"+c.ID, true)
	return op, outcome, nil
}

// A code-only publication keeps the active configuration's identity when all
// bindings are still identical. External API changes naturally break this
// association; protocol requests do not need a configuration_id field.
func inheritedConfiguration(st *domain.State, env domain.WorkerEnvironment) string {
	previous := st.Workers.Versions[env.Version]
	configuration, exists := st.Workers.Configurations[previous.Configuration]
	if !exists {
		return ""
	}
	if configuration.VersionMetadata != env.VersionMetadata || configuration.AssetsBinding != env.AssetsBinding {
		return ""
	}
	if !reflect.DeepEqual(domain.CloneWorkerVariables(configuration.Variables), domain.CloneWorkerVariables(env.Variables)) ||
		!reflect.DeepEqual(domain.CloneR2Bindings(configuration.R2Bindings), domain.CloneR2Bindings(env.R2Bindings)) ||
		!maps.Equal(configuration.HyperdriveBindings, env.HyperdriveBindings) ||
		!reflect.DeepEqual(domain.CloneStringsMap(configuration.SecretVersions), domain.CloneStringsMap(env.SecretVersions)) {
		return ""
	}
	return configuration.ID
}
