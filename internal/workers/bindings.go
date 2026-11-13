package workers

import (
	"context"
	"maps"
	"strings"

	workerartifact "github.com/krun-dev/krun/internal/artifact"
	"github.com/krun-dev/krun/internal/authorization"
	appconfig "github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/domain"
)

// StageBindings changes desired configuration; callers explicitly snapshot and
// deploy afterward. A nil secret deletes it, while omitted secrets are retained.
type BindingsInput struct {
	ServiceBindings     *map[string]domain.WorkerServiceBinding
	KeepServiceBindings bool
	R2Buckets           *map[string]string
	KeepR2Buckets       bool
	HyperdriveBindings  *map[string]string
	KeepHyperdrive      bool
	VersionMetadata     *string
	AssetsBinding       *string
	Variables           map[string]any
	Secrets             map[string]*string
	ReplaceVariables    bool
	KeepPlainVariables  bool
	KeepJSONVariables   bool
	KeepSecrets         bool
	Inherit             []string
}

func (s *Service) StageBindings(ctx context.Context, st *domain.State, p authorization.Principal, ref WorkerRef, in BindingsInput) error {
	env := ref.Environment
	action := "worker.publish"
	authEnv := env
	if env == domain.PreviewBaseName {
		action = "worker.update"
		authEnv = "global"
	}
	target, err := s.WorkerTarget(st, ref, authEnv, action, p)
	if err != nil {
		return err
	}
	worker := s.Worker(st, target)
	current, ok := worker.Environments[env]
	if env == domain.PreviewBaseName {
		current = domain.WorkerEnvironment{Name: env, Enabled: true, Variables: map[string]any{}, SecretVersions: map[string]string{}}
		if worker.PreviewBase != nil {
			current = *worker.PreviewBase
		}
		ok = true
	}
	if !ok {
		return domain.NotFound()
	}
	if WorkerBusy(st, target.ID, env) {
		return domain.Conflict("DEPLOYMENT_BUSY", "Wait for the active deployment")
	}
	for _, name := range in.Inherit {
		if binding, exists := current.ServiceBindings[name]; exists {
			if in.ServiceBindings == nil {
				bindings := map[string]domain.WorkerServiceBinding{}
				in.ServiceBindings = &bindings
			}
			(*in.ServiceBindings)[name] = binding
			continue
		}
		if id, exists := current.HyperdriveBindings[name]; exists {
			if in.HyperdriveBindings == nil {
				bindings := map[string]string{}
				in.HyperdriveBindings = &bindings
			}
			(*in.HyperdriveBindings)[name] = id
			continue
		}
		if binding, exists := current.R2Bindings[name]; exists {
			if in.R2Buckets == nil {
				values := map[string]string{}
				in.R2Buckets = &values
			}
			(*in.R2Buckets)[name] = binding.BucketName
			continue
		}
		if name == current.VersionMetadata {
			value := name
			in.VersionMetadata = &value
			continue
		}
		if name == current.AssetsBinding {
			value := name
			in.AssetsBinding = &value
			continue
		}
		if _, secret := current.SecretVersions[name]; !secret {
			if _, plain := current.Variables[name]; !plain {
				return domain.Invalid("WORKER_BINDINGS_INVALID", "Inherited binding does not exist")
			}
		}
	}
	vars := domain.CloneWorkerVariables(current.Variables)
	if in.ReplaceVariables {
		for name, value := range vars {
			_, plain := value.(string)
			if plain && !in.KeepPlainVariables || !plain && !in.KeepJSONVariables {
				delete(vars, name)
			}
		}
	}
	for _, name := range in.Inherit {
		if value, ok := current.Variables[name]; ok {
			vars[name] = value
		}
	}
	secrets := domain.CloneStringsMap(current.SecretVersions)
	if !in.KeepSecrets {
		secrets = map[string]string{}
	}
	for name, value := range in.Variables {
		vars[name] = value
		delete(secrets, name)
	}
	if !appconfig.WorkerVariablesValid(vars) {
		return domain.Invalid("WORKER_BINDINGS_INVALID", "Invalid variable names or size")
	}
	for name, value := range in.Secrets {
		if !workerSecretInputValid(name, value) {
			return domain.Invalid("WORKER_SECRET_INVALID", "Invalid secret name or size")
		}
		if value == nil {
			delete(secrets, name)
			delete(vars, name)
			continue
		}
		delete(vars, name)
		id, err := s.stageWorkerSecret(st, target, env, name, p.User.ID, *value)
		if err != nil {
			return err
		}
		secrets[name] = id
	}
	if len(secrets) > WorkerSecretMaxCount {
		return domain.Invalid("WORKER_SECRET_INVALID", "At most 100 secrets per environment")
	}
	for name := range secrets {
		if strings.HasPrefix(name, "KRUN_") {
			return domain.Invalid("WORKER_BINDINGS_INVALID", "Reserved binding")
		}
	}
	current.Variables = vars
	current.SecretVersions = secrets
	if in.R2Buckets != nil && in.KeepR2Buckets {
		for name, binding := range current.R2Bindings {
			if _, exists := (*in.R2Buckets)[name]; !exists {
				(*in.R2Buckets)[name] = binding.BucketName
			}
		}
	}
	if in.R2Buckets != nil && len(*in.R2Buckets) > 0 && !s.r2BindingStorageConfigured() {
		return domain.Unavailable("OBJECT_STORAGE_NOT_CONFIGURED", "R2 storage and its cursor key must be configured")
	}
	if in.HyperdriveBindings != nil && in.KeepHyperdrive {
		bindings := maps.Clone(*in.HyperdriveBindings)
		for name, id := range current.HyperdriveBindings {
			if _, exists := bindings[name]; !exists {
				bindings[name] = id
			}
		}
		in.HyperdriveBindings = &bindings
	}
	if err := s.applyBindingPatch(ctx, st, target.Organization, &current, in.R2Buckets, in.HyperdriveBindings, in.VersionMetadata); err != nil {
		return err
	}
	if in.ServiceBindings != nil {
		if in.KeepServiceBindings {
			for name, binding := range current.ServiceBindings {
				if _, ok := (*in.ServiceBindings)[name]; !ok {
					(*in.ServiceBindings)[name] = binding
				}
			}
		}
		bindings, err := s.resolveServiceBindings(ctx, p, target, *in.ServiceBindings)
		if err != nil {
			return err
		}
		current.ServiceBindings = bindings
	}
	if in.AssetsBinding != nil {
		current.AssetsBinding = *in.AssetsBinding
	}
	if err := ValidateWorkerBindings(current); err != nil {
		return err
	}
	current.SecretNames = domain.WorkerSecretNames(secrets)
	current.ConfigRevision++
	if env == domain.PreviewBaseName {
		worker.PreviewBase = &current
	} else {
		worker.Environments[env] = current
	}
	st.Workers.Workers[target.ID] = worker
	s.audit(st, p, "workers.bindings.change", target.ID+"/"+env, true)
	return nil
}

func (s *Service) r2BindingStorageConfigured() bool {
	workers := s.Config.Workers
	return workers != nil && workers.ObjectStorage != nil && workers.ObjectStorage.CursorKey != ""
}

func (s *Service) applyBindingPatch(
	ctx context.Context, st *domain.State, organization string, env *domain.WorkerEnvironment,
	r2Buckets, hyperdriveBindings *map[string]string, versionMetadata *string,
) error {
	if r2Buckets != nil {
		resolved, err := resolveR2Bindings(st, organization, *r2Buckets)
		if err != nil {
			return err
		}
		env.R2Bindings = resolved
	}
	if hyperdriveBindings != nil {
		if err := s.validateHyperdriveBindings(ctx, organization, *hyperdriveBindings); err != nil {
			return err
		}
		env.HyperdriveBindings = cloneHyperdriveBindings(*hyperdriveBindings)
	}
	if versionMetadata != nil {
		env.VersionMetadata = *versionMetadata
	}
	return nil
}

func cloneHyperdriveBindings(bindings map[string]string) map[string]string {
	if len(bindings) == 0 {
		return nil
	}
	return maps.Clone(bindings)
}

func ValidateWorkerBindings(env domain.WorkerEnvironment) error {
	seen := map[string]bool{}
	for name := range env.Variables {
		seen[name] = true
	}
	for name := range env.SecretVersions {
		if seen[name] {
			return domain.Conflict("WORKER_BINDING_CONFLICT", "Binding names must be unique")
		}
		seen[name] = true
	}
	for _, name := range []string{env.VersionMetadata, env.AssetsBinding} {
		if name == "" {
			continue
		}
		if !workerartifact.WorkerAssetBindingName.MatchString(name) || strings.HasPrefix(name, "KRUN_") || workerartifact.WorkerReservedBindings[name] || seen[name] {
			return domain.Invalid("WORKER_BINDING_CONFLICT", "Invalid or duplicate binding name")
		}
		seen[name] = true
	}
	for name, binding := range env.R2Bindings {
		if !workerartifact.WorkerAssetBindingName.MatchString(name) || strings.HasPrefix(name, "KRUN_") || workerartifact.WorkerReservedBindings[name] || seen[name] || binding.BucketID == "" || binding.BucketName == "" {
			return domain.Invalid("WORKER_BINDING_CONFLICT", "Invalid or duplicate R2 binding")
		}
		seen[name] = true
	}
	if len(env.ServiceBindings) > 16 {
		return domain.Invalid("WORKER_BINDINGS_INVALID", "Too many service bindings")
	}
	for name := range env.ServiceBindings {
		if !workerartifact.WorkerAssetBindingName.MatchString(name) || strings.HasPrefix(name, "KRUN_") || workerartifact.WorkerReservedBindings[name] || seen[name] {
			return domain.Invalid("WORKER_BINDING_CONFLICT", "Invalid or duplicate service binding")
		}
		seen[name] = true
	}
	if len(env.HyperdriveBindings) > 32 {
		return domain.Invalid("WORKER_BINDINGS_INVALID", "Too many Hyperdrive bindings")
	}
	for name, id := range env.HyperdriveBindings {
		if !workerartifact.WorkerAssetBindingName.MatchString(name) || strings.HasPrefix(name, "KRUN_") || workerartifact.WorkerReservedBindings[name] || seen[name] || len(id) != 32 {
			return domain.Invalid("WORKER_BINDING_CONFLICT", "Invalid or duplicate Hyperdrive binding")
		}
	}
	return nil
}
