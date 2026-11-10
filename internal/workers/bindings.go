package workers

import (
	"context"
	workerartifact "github.com/krun-dev/krun/internal/artifact"
	"maps"
	"strings"

	"github.com/krun-dev/krun/internal/authorization"
	appconfig "github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/domain"
)

// StageBindings changes desired configuration; callers explicitly snapshot and
// deploy afterward. A nil secret deletes it, while omitted secrets are retained.
type BindingsInput struct {
	R2Buckets          *map[string]string
	KeepR2Buckets      bool
	HyperdriveBindings *map[string]string
	KeepHyperdrive     bool
	VersionMetadata    *string
	AssetsBinding      *string
	Variables          map[string]any
	Secrets            map[string]*string
	ReplaceVariables   bool
	KeepPlainVariables bool
	KeepJSONVariables  bool
	KeepSecrets        bool
	Inherit            []string
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
		return domain.Problem(409, "DEPLOYMENT_BUSY", "Wait for the active deployment")
	}
	for _, name := range in.Inherit {
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
				return domain.Problem(422, "WORKER_BINDINGS_INVALID", "Inherited binding does not exist")
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
		return domain.Problem(422, "WORKER_BINDINGS_INVALID", "Invalid variable names or size")
	}
	for name, value := range in.Secrets {
		if !WorkerSecretNameValid(name) || value != nil && len(*value) > WorkerSecretMaxBytes {
			return domain.Problem(422, "WORKER_SECRET_INVALID", "Invalid secret name or size")
		}
		if value == nil {
			delete(secrets, name)
			delete(vars, name)
			continue
		}
		delete(vars, name)
		encrypted, err := EncryptWorkerSecret(s.Config.Workers, target.Organization, target.ID, env, name, p.User.ID, *value)
		if err != nil {
			return domain.Problem(503, "WORKER_SECRETS_NOT_CONFIGURED", "Worker secret encryption is not configured")
		}
		st.Workers.SecretVersions[encrypted.Version.ID] = encrypted.Version
		if st.PendingWorkerSecrets == nil {
			st.PendingWorkerSecrets = map[string]domain.EncryptedWorkerSecret{}
		}
		st.PendingWorkerSecrets[encrypted.Version.ID] = encrypted
		secrets[name] = encrypted.Version.ID
	}
	if len(secrets) > WorkerSecretMaxCount {
		return domain.Problem(422, "WORKER_SECRET_INVALID", "At most 100 secrets per environment")
	}
	for name := range secrets {
		if strings.HasPrefix(name, "KRUN_") {
			return domain.Problem(422, "WORKER_BINDINGS_INVALID", "Reserved binding")
		}
	}
	current.Variables = vars
	current.SecretVersions = secrets
	if in.R2Buckets != nil {
		if in.KeepR2Buckets {
			for name, binding := range current.R2Bindings {
				if _, exists := (*in.R2Buckets)[name]; !exists {
					(*in.R2Buckets)[name] = binding.BucketName
				}
			}
		}
		if len(*in.R2Buckets) > 0 && (s.Config.Workers == nil || s.Config.Workers.ObjectStorage == nil || s.Config.Workers.ObjectStorage.CursorKey == "") {
			return domain.Problem(503, "OBJECT_STORAGE_NOT_CONFIGURED", "R2 storage and its cursor key must be configured")
		}
		resolved, err := resolveR2Bindings(st, target.Organization, *in.R2Buckets)
		if err != nil {
			return err
		}
		current.R2Bindings = resolved
	}
	if in.HyperdriveBindings != nil {
		bindings := maps.Clone(*in.HyperdriveBindings)
		if in.KeepHyperdrive {
			for name, id := range current.HyperdriveBindings {
				if _, exists := bindings[name]; !exists {
					bindings[name] = id
				}
			}
		}
		if err := s.validateHyperdriveBindings(ctx, target.Organization, bindings); err != nil {
			return err
		}
		current.HyperdriveBindings = cloneHyperdriveBindings(bindings)
	}
	if in.VersionMetadata != nil {
		current.VersionMetadata = *in.VersionMetadata
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
			return domain.Problem(409, "WORKER_BINDING_CONFLICT", "Binding names must be unique")
		}
		seen[name] = true
	}
	for _, name := range []string{env.VersionMetadata, env.AssetsBinding} {
		if name == "" {
			continue
		}
		if !workerartifact.WorkerAssetBindingName.MatchString(name) || strings.HasPrefix(name, "KRUN_") || workerartifact.WorkerReservedBindings[name] || seen[name] {
			return domain.Problem(422, "WORKER_BINDING_CONFLICT", "Invalid or duplicate binding name")
		}
		seen[name] = true
	}
	for name, binding := range env.R2Bindings {
		if !workerartifact.WorkerAssetBindingName.MatchString(name) || strings.HasPrefix(name, "KRUN_") || workerartifact.WorkerReservedBindings[name] || seen[name] || binding.BucketID == "" || binding.BucketName == "" {
			return domain.Problem(422, "WORKER_BINDING_CONFLICT", "Invalid or duplicate R2 binding")
		}
		seen[name] = true
	}
	if len(env.HyperdriveBindings) > 32 {
		return domain.Problem(422, "WORKER_BINDINGS_INVALID", "Too many Hyperdrive bindings")
	}
	for name, id := range env.HyperdriveBindings {
		if !workerartifact.WorkerAssetBindingName.MatchString(name) || strings.HasPrefix(name, "KRUN_") || workerartifact.WorkerReservedBindings[name] || seen[name] || len(id) != 32 {
			return domain.Problem(422, "WORKER_BINDING_CONFLICT", "Invalid or duplicate Hyperdrive binding")
		}
	}
	return nil
}
