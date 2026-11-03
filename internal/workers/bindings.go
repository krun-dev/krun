package workers

import (
	"strings"

	"github.com/krun-dev/krun/internal/authorization"
	appconfig "github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/domain"
)

// StageBindings changes desired configuration; callers explicitly snapshot and
// deploy afterward. A nil secret deletes it, while omitted secrets are retained.
type BindingsInput struct {
	Variables        map[string]string
	Secrets          map[string]*string
	ReplaceVariables bool
	KeepSecrets      bool
	Inherit          []string
}

func (s *Service) StageBindings(st *domain.State, p authorization.Principal, ref ServiceRef, in BindingsInput) error {
	env := ref.Environment
	action := "service.publish"
	authEnv := env
	if env == domain.PreviewBaseName {
		action = "service.update"
		authEnv = "global"
	}
	target, err := s.WorkerTarget(st, ref, authEnv, action, p)
	if err != nil {
		return err
	}
	service := s.WorkerService(st, target)
	current, ok := service.Environments[env]
	if env == domain.PreviewBaseName {
		current = domain.WorkerEnvironment{Name: env, Enabled: true, Variables: map[string]string{}, SecretVersions: map[string]string{}}
		if service.PreviewBase != nil {
			current = *service.PreviewBase
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
		if _, secret := current.SecretVersions[name]; !secret {
			if _, plain := current.Variables[name]; !plain {
				return domain.Problem(422, "WORKER_BINDINGS_INVALID", "Inherited binding does not exist")
			}
		}
	}
	vars := domain.CloneStringsMap(current.Variables)
	if in.ReplaceVariables {
		vars = map[string]string{}
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
	current.SecretNames = domain.WorkerSecretNames(secrets)
	current.ConfigRevision++
	if env == domain.PreviewBaseName {
		service.PreviewBase = &current
	} else {
		service.Environments[env] = current
	}
	st.Workers.Services[target.ID] = service
	s.audit(st, p, "workers.bindings.change", target.ID+"/"+env, true)
	return nil
}
