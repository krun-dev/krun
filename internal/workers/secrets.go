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
	WorkerSecretMaxCount = 32
	WorkerSecretMaxBytes = 16 << 10
)

var WorkerBindingName = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)

type WorkerSecretInput struct {
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

func WorkerSecretNameValid(name string) bool {
	return WorkerBindingName.MatchString(name) && !strings.HasPrefix(name, "KRUN_")
}

func WorkerSecretAAD(version domain.WorkerSecretVersion) []byte {
	return []byte(strings.Join([]string{
		"krun-worker-secret-v1", version.ID, version.Organization, version.Service,
		version.Environment, version.Name, version.KeyVersion,
	}, "\x00"))
}

func EncryptWorkerSecret(config *appconfig.WorkersConfig, organization, service, environment, name, actor, value string) (domain.EncryptedWorkerSecret, error) {
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
		ID: securetoken.NewID("secv"), Organization: organization, Service: service,
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

func (s *Service) WorkerSecretList(ctx context.Context, st *domain.State, p authorization.Principal, ref ServiceRef) (any, Outcome, error) {
	environment := ref.Environment
	if !appconfig.WorkerEnvValid(environment) {
		return nil, Read, domain.NotFound()
	}
	target, err := s.WorkerTarget(st, ref, environment, "service.read", p)
	if err != nil {
		return nil, Read, err
	}
	service := s.WorkerService(st, target)
	current, ok := service.Environments[environment]
	if !ok {
		return nil, Read, domain.NotFound()
	}
	summaries := make([]WorkerSecretSummary, 0, len(current.SecretVersions))
	for name, versionID := range current.SecretVersions {
		version, ok := st.Workers.SecretVersions[versionID]
		if !ok || version.Service != target.ID || version.Environment != environment || version.Name != name {
			return nil, Read, errors.New("Worker secret metadata is inconsistent")
		}
		summaries = append(summaries, WorkerSecretSummary{Name: name, CreatedAt: version.Created})
	}
	sort.Slice(summaries, func(i, j int) bool { return summaries[i].Name < summaries[j].Name })
	return collection(summaries)

}

func (s *Service) WorkerSecretChange(ctx context.Context, st *domain.State, p authorization.Principal, ref ServiceRef, target authorization.Target, environment string, name string, deleting bool, input WorkerSecretInput) (any, Outcome, error) {
	if input.Key == "" || len(input.Key) > 128 || input.Revision < 0 {
		return nil, Read, domain.Problem(422, "WORKER_SECRET_INVALID", "expected_revision and idempotency_key required")
	}
	if !deleting && (len(input.Value) == 0 || len(input.Value) > WorkerSecretMaxBytes) {
		return nil, Read, domain.Problem(422, "WORKER_SECRET_INVALID", "Secret value must contain 1–16384 bytes")
	}
	kind := "secret-put:" + name
	if deleting {
		kind = "secret-delete:" + name
	}
	idempotencyKey := WorkerIdempotencyKey(target, p, kind, environment, input.Key)
	operationID, err := WorkerIdempotent(st, idempotencyKey, input)
	if err != nil {
		return nil, Read, err
	}
	if operationID != "" {
		return st.Workers.Operations[operationID], Read, nil
	}
	service := s.WorkerService(st, target)
	current, ok := service.Environments[environment]
	if !ok {
		return nil, Read, domain.NotFound()
	}
	if !current.Enabled {
		return nil, Read, domain.Problem(409, "ENVIRONMENT_DISABLED", "Target environment is disabled")
	}
	if current.Revision != input.Revision {
		return nil, Read, domain.Problem(409, "REVISION_CONFLICT", "Environment release changed")
	}
	if WorkerBusy(st, target.ID, environment) {
		return nil, Read, domain.Problem(409, "RELEASE_BUSY", "Wait for the active release operation")
	}
	active, ok := st.Workers.Releases[current.Release]
	if !ok || current.Release == "" || active.Artifact == "" {
		return nil, Read, domain.Problem(409, "ENVIRONMENT_NOT_PUBLISHED", "Publish the Worker before configuring secrets")
	}
	if _, collision := current.Variables[name]; collision && !deleting {
		return nil, Read, domain.Problem(409, "WORKER_BINDING_CONFLICT", "A plain variable already uses this binding name")
	}
	versions := domain.CloneStringsMap(current.SecretVersions)
	if deleting {
		if _, exists := versions[name]; !exists {
			return nil, Read, domain.NotFound()
		}
		delete(versions, name)
	} else {
		if len(versions) >= WorkerSecretMaxCount {
			if _, exists := versions[name]; !exists {
				return nil, Read, domain.Problem(422, "WORKER_SECRET_INVALID", "At most 32 secrets are supported per environment")
			}
		}
		encrypted, encryptErr := EncryptWorkerSecret(s.Config.Workers, target.Organization, target.ID, environment, name, p.User.ID, input.Value)
		if encryptErr != nil {
			return nil, Read, domain.Problem(503, "WORKER_SECRETS_NOT_CONFIGURED", "Worker secret encryption is not configured")
		}
		st.Workers.SecretVersions[encrypted.Version.ID] = encrypted.Version
		if st.PendingWorkerSecrets == nil {
			st.PendingWorkerSecrets = map[string]domain.EncryptedWorkerSecret{}
		}
		st.PendingWorkerSecrets[encrypted.Version.ID] = encrypted
		versions[name] = encrypted.Version.ID
	}

	current.SecretVersions = versions
	current.SecretNames = domain.WorkerSecretNames(versions)
	current.ConfigRevision++
	service.Environments[environment] = current
	st.Workers.Services[target.ID] = service

	release := domain.WorkerRelease{
		Service: target.ID, Organization: target.Organization, Environment: environment,
		Artifact: active.Artifact, Profile: active.Profile, ArchiveDigest: active.ArchiveDigest, ModuleDigest: active.ModuleDigest,
		CompatibilityDate: active.CompatibilityDate, CompatibilityFlags: append([]string(nil), active.CompatibilityFlags...),
		ConfigRevision: current.ConfigRevision, Variables: domain.CloneStringsMap(current.Variables),
		SecretDigest: WorkerSecretsDigest(versions), SecretVersions: domain.CloneStringsMap(versions),
		Created: time.Now().UTC(),
	}
	identity := release
	identity.Created = time.Time{}
	raw, _ := json.Marshal(identity)
	release.ID = "rel_" + workerartifact.BytesDigest(raw)
	if existing, exists := st.Workers.Releases[release.ID]; exists {
		release = existing
	} else {
		st.Workers.Releases[release.ID] = release
	}

	operationID = securetoken.NewID("op")
	operation := domain.WorkerOperation{
		ID: operationID, Kind: "secret", Organization: target.Organization, Service: target.ID,
		Environment: environment, State: "queued", Release: release.ID,
		ExpectedRevision: input.Revision, ConfigRevision: current.ConfigRevision,
		Steps: []domain.WorkerStep{{Name: "artifact", State: "pending"}, {Name: "candidate", State: "pending"}, {Name: "commit", State: "pending"}, {Name: "synchronize", State: "pending"}}, Created: time.Now().UTC(),
	}
	st.Workers.Operations[operationID] = operation
	st.Workers.OperationPrivate[operationID] = domain.WorkerOperationPrivate{Actor: WorkerActor(p)}
	WorkerRemember(st, idempotencyKey, input, operationID)
	action := "workers.secret.put"
	if deleting {
		action = "workers.secret.delete"
	}
	s.audit(st, p, action, target.ID+"/"+environment+"/"+name, true)
	return operation, Queued, nil

}
