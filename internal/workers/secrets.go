package workers

import (
	context "context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"

	workerartifact "github.com/krun-dev/krun/internal/artifact"
	authorization "github.com/krun-dev/krun/internal/authorization"
	appconfig "github.com/krun-dev/krun/internal/config"
	domain "github.com/krun-dev/krun/internal/domain"
	securetoken "github.com/krun-dev/krun/internal/securetoken"
)

const (
	WorkerSecretMaxCount = 100
	WorkerSecretMaxBytes = 16 << 10
)

var WorkerBindingName = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]{0,127}$`)

type WorkerSecretInput struct {
	Deploy   *bool  `json:"deploy,omitempty"`
	Value    string `json:"value,omitempty"`
	Revision int    `json:"expected_revision"`
	Key      string `json:"idempotency_key"`
}

type WorkerSecretSummary struct {
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

func WorkerSecretsDigest(versions map[string]string) string {
	if len(versions) == 0 {
		return ""
	}
	raw, _ := json.Marshal(versions)
	return workerartifact.BytesDigest(raw)
}

// DecryptWorkerSecrets resolves exactly one immutable Version's secret bindings.
func DecryptWorkerSecrets(config *appconfig.WorkersConfig, digest string, encrypted []domain.EncryptedWorkerSecret) (map[string]string, error) {
	values := make(map[string]string, len(encrypted))
	versions := make(map[string]string, len(encrypted))
	for _, item := range encrypted {
		value, err := DecryptWorkerSecret(config, item)
		if err != nil {
			return nil, domain.Problem(503, "WORKER_SECRET_UNAVAILABLE", "Worker secret unavailable")
		}
		values[item.Version.Name] = value
		versions[item.Version.Name] = item.Version.ID
	}
	if WorkerSecretsDigest(versions) != digest {
		return nil, domain.Problem(503, "WORKER_SECRET_UNAVAILABLE", "Worker secret binding mismatch")
	}
	return values, nil
}

func WorkerSecretNameValid(name string) bool {
	return WorkerBindingName.MatchString(name) && !strings.HasPrefix(name, "KRUN_")
}

func WorkerSecretAAD(version domain.WorkerSecretVersion) []byte {
	return []byte(strings.Join([]string{
		"krun-worker-secret-v1", version.ID, version.Organization, version.Worker,
		version.Environment, version.Name, version.KeyVersion,
	}, "\x00"))
}

func EncryptWorkerSecret(config *appconfig.WorkersConfig, organization, worker, environment, name, actor, value string) (domain.EncryptedWorkerSecret, error) {
	key, err := config.WorkerSecretKey(config.SecretsActiveKey)
	if err != nil {
		return domain.EncryptedWorkerSecret{}, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return domain.EncryptedWorkerSecret{}, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return domain.EncryptedWorkerSecret{}, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return domain.EncryptedWorkerSecret{}, err
	}
	digest := hmac.New(sha256.New, key)
	_, _ = digest.Write([]byte(value))
	version := domain.WorkerSecretVersion{
		ID: securetoken.NewID("secv"), Organization: organization, Worker: worker,
		Environment: environment, Name: name, ValueDigest: hex.EncodeToString(digest.Sum(nil)),
		KeyVersion: config.SecretsActiveKey, CreatedBy: actor, Created: time.Now().UTC(),
	}
	ciphertext := aead.Seal(nil, nonce, []byte(value), WorkerSecretAAD(version))
	return domain.EncryptedWorkerSecret{Version: version, Ciphertext: ciphertext, Nonce: nonce}, nil
}

func DecryptWorkerSecret(config *appconfig.WorkersConfig, encrypted domain.EncryptedWorkerSecret) (string, error) {
	key, err := config.WorkerSecretKey(encrypted.Version.KeyVersion)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(encrypted.Nonce) != aead.NonceSize() {
		return "", errors.New("invalid Worker secret nonce")
	}
	plaintext, err := aead.Open(nil, encrypted.Nonce, encrypted.Ciphertext, WorkerSecretAAD(encrypted.Version))
	if err != nil {
		return "", errors.New("Worker secret authentication failed")
	}
	digest := hmac.New(sha256.New, key)
	_, _ = digest.Write(plaintext)
	want, decodeErr := hex.DecodeString(encrypted.Version.ValueDigest)
	if decodeErr != nil || !hmac.Equal(digest.Sum(nil), want) {
		return "", errors.New("Worker secret digest mismatch")
	}
	return string(plaintext), nil
}

func (s *Service) WorkerSecretList(ctx context.Context, st *domain.State, p authorization.Principal, ref WorkerRef) (any, Outcome, error) {
	environment := ref.Environment
	if !appconfig.WorkerEnvValid(environment) {
		return nil, Read, domain.NotFound()
	}
	target, err := s.WorkerTarget(st, ref, environment, "worker.read", p)
	if err != nil {
		return nil, Read, err
	}
	worker := s.Worker(st, target)
	current, ok := worker.Environments[environment]
	if !ok {
		return nil, Read, domain.NotFound()
	}
	summaries := make([]WorkerSecretSummary, 0, len(current.SecretVersions))
	for name, versionID := range current.SecretVersions {
		version, ok := st.Workers.SecretVersions[versionID]
		if !ok || version.Worker != target.ID || (version.Environment != environment && !(environment != appconfig.WorkerBaseline && version.Environment == domain.PreviewBaseName)) || version.Name != name {
			return nil, Read, errors.New("Worker secret metadata is inconsistent")
		}
		summaries = append(summaries, WorkerSecretSummary{Name: name, CreatedAt: version.Created})
	}
	sort.Slice(summaries, func(i, j int) bool { return summaries[i].Name < summaries[j].Name })
	return collection(summaries)

}

// WorkerSecretChange is the single-key convenience form of a bounded batch.
func (s *Service) WorkerSecretChange(ctx context.Context, st *domain.State, p authorization.Principal, ref WorkerRef, target authorization.Target, environment, name string, deleting bool, in WorkerSecretInput) (any, Outcome, error) {
	value := &in.Value
	if deleting {
		value = nil
	}
	return s.WorkerSecretBatch(ctx, st, p, ref, target, environment, WorkerSecretBatchInput{Secrets: map[string]*string{name: value}, Revision: in.Revision, Key: in.Key, Deploy: in.Deploy})
}

type WorkerSecretBatchInput struct {
	Secrets  map[string]*string `json:"secrets"`
	Revision int                `json:"expected_revision"`
	Key      string             `json:"idempotency_key"`
	Deploy   *bool              `json:"deploy,omitempty"`
}

func (s *Service) WorkerSecretBatch(ctx context.Context, st *domain.State, p authorization.Principal, ref WorkerRef, target authorization.Target, environment string, in WorkerSecretBatchInput) (any, Outcome, error) {
	if in.Key == "" || len(in.Key) > 128 || in.Revision < 0 || len(in.Secrets) == 0 || len(in.Secrets) > WorkerSecretMaxCount {
		return nil, Read, domain.Problem(422, "WORKER_SECRET_INVALID", "A bounded secret batch, expected_revision and idempotency_key are required")
	}
	key := WorkerIdempotencyKey(target, p, "secret-batch", environment, in.Key)
	if id, err := WorkerIdempotent(st, key, in); err != nil {
		return nil, Read, err
	} else if id != "" {
		if op, ok := st.Workers.Operations[id]; ok {
			return op, Read, nil
		}
		if v, ok := st.Workers.Versions[id]; ok {
			return v, Read, nil
		}
		return map[string]any{"configuration_revision": id}, Read, nil
	}
	worker := s.Worker(st, target)
	current, ok := worker.Environments[environment]
	base := environment == domain.PreviewBaseName
	if base {
		current = domain.WorkerEnvironment{Name: domain.PreviewBaseName, Enabled: true, Variables: map[string]any{}, SecretVersions: map[string]string{}}
		if worker.PreviewBase != nil {
			current = *worker.PreviewBase
		}
		ok = true
	}
	if !ok {
		return nil, Read, domain.NotFound()
	}
	if !current.Enabled {
		return nil, Read, domain.Problem(409, "ENVIRONMENT_DISABLED", "Target environment is disabled")
	}
	if current.Revision != in.Revision {
		return nil, Read, domain.Problem(409, "REVISION_CONFLICT", "Environment deployment changed")
	}
	if !base && WorkerBusy(st, target.ID, environment) {
		return nil, Read, domain.Problem(409, "DEPLOYMENT_BUSY", "Wait for the active deployment")
	}
	versions := domain.CloneStringsMap(current.SecretVersions)
	for name, value := range in.Secrets {
		if !WorkerSecretNameValid(name) || value != nil && len(*value) > WorkerSecretMaxBytes {
			return nil, Read, domain.Problem(422, "WORKER_SECRET_INVALID", "Invalid secret name or value size")
		}
		if value == nil {
			if _, ok := versions[name]; !ok {
				return nil, Read, domain.NotFound()
			}
			delete(versions, name)
			continue
		}
		if _, collision := current.Variables[name]; collision {
			return nil, Read, domain.Problem(409, "WORKER_BINDING_CONFLICT", "A plain variable already uses this binding name")
		}
		encrypted, err := EncryptWorkerSecret(s.Config.Workers, target.Organization, target.ID, environment, name, p.User.ID, *value)
		if err != nil {
			return nil, Read, domain.Problem(503, "WORKER_SECRETS_NOT_CONFIGURED", "Worker secret encryption is not configured")
		}
		st.Workers.SecretVersions[encrypted.Version.ID] = encrypted.Version
		if st.PendingWorkerSecrets == nil {
			st.PendingWorkerSecrets = map[string]domain.EncryptedWorkerSecret{}
		}
		st.PendingWorkerSecrets[encrypted.Version.ID] = encrypted
		versions[name] = encrypted.Version.ID
	}
	if len(versions) > WorkerSecretMaxCount {
		return nil, Read, domain.Problem(422, "WORKER_SECRET_INVALID", "At most 100 secrets are supported per environment")
	}
	current.SecretVersions = versions
	if err := ValidateWorkerBindings(current); err != nil {
		return nil, Read, err
	}
	current.SecretNames = domain.WorkerSecretNames(versions)
	current.ConfigRevision++
	if base {
		worker.PreviewBase = &current
	} else {
		worker.Environments[environment] = current
	}
	st.Workers.Workers[target.ID] = worker
	s.audit(st, p, "workers.secret.change", target.ID+"/"+environment, true)
	active, published := st.Workers.Versions[current.Version]
	if base || !published {
		id := fmt.Sprint(current.ConfigRevision)
		WorkerRemember(st, key, in, id)
		return map[string]any{"configuration_revision": id, "secret_names": current.SecretNames}, Created, nil
	}
	artifact, ok := st.Workers.Artifacts[active.Artifact]
	if !ok {
		return nil, Read, domain.Problem(409, "ARTIFACT_UNAVAILABLE", "Artifact unavailable")
	}
	version := SnapshotVersion(target.Organization, target.ID, environment, artifact, current)
	st.Workers.Versions[version.ID] = version
	if in.Deploy != nil && !*in.Deploy {
		WorkerRemember(st, key, in, version.ID)
		return version, Created, nil
	}
	operationID := securetoken.NewID("op")
	op := domain.WorkerOperation{ID: operationID, Kind: "secret", Organization: target.Organization, Worker: target.ID, Environment: environment, State: "queued", Version: version.ID, ExpectedRevision: in.Revision, ConfigRevision: current.ConfigRevision, Steps: []domain.WorkerStep{{Name: "artifact", State: "pending"}, {Name: "candidate", State: "pending"}, {Name: "commit", State: "pending"}, {Name: "synchronize", State: "pending"}}, Created: time.Now().UTC()}
	st.Workers.Operations[op.ID] = op
	RememberDeployment(st, op)
	st.Workers.OperationPrivate[op.ID] = domain.WorkerOperationPrivate{Actor: WorkerActor(p)}
	WorkerRemember(st, key, in, op.ID)
	return op, Queued, nil
}
