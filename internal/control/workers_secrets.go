package control

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	workerSecretMaxCount = 32
	workerSecretMaxBytes = 16 << 10
)

var workerBindingName = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)

type workerSecretInput struct {
	Value    string `json:"value,omitempty"`
	Revision int    `json:"expected_revision"`
	Key      string `json:"idempotency_key"`
}

type workerSecretSummary struct {
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

func workerSecretsDigest(versions map[string]string) string {
	if len(versions) == 0 {
		return ""
	}
	raw, _ := json.Marshal(versions)
	return bytesDigest(raw)
}

func workerSecretNameValid(name string) bool {
	return workerBindingName.MatchString(name) && !strings.HasPrefix(name, "KRUN_")
}

func (c *WorkersConfig) workerSecretKey(version string) ([]byte, error) {
	if c == nil || version == "" || len(c.SecretsKeys) == 0 {
		return nil, errors.New("Workers secrets are not configured")
	}
	encoded, ok := c.SecretsKeys[version]
	if !ok {
		return nil, errors.New("Workers secret key version unavailable")
	}
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(key) != 32 {
		return nil, errors.New("Workers secret key is invalid")
	}
	return key, nil
}

func workerSecretAAD(version WorkerSecretVersion) []byte {
	return []byte(strings.Join([]string{
		"krun-worker-secret-v1", version.ID, version.Organization, version.Service,
		version.Environment, version.Name, version.KeyVersion,
	}, "\x00"))
}

func encryptWorkerSecret(config *WorkersConfig, organization, service, environment, name, actor, value string) (encryptedWorkerSecret, error) {
	key, err := config.workerSecretKey(config.SecretsActiveKey)
	if err != nil {
		return encryptedWorkerSecret{}, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return encryptedWorkerSecret{}, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return encryptedWorkerSecret{}, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return encryptedWorkerSecret{}, err
	}
	digest := hmac.New(sha256.New, key)
	_, _ = digest.Write([]byte(value))
	version := WorkerSecretVersion{
		ID: randomID("secv"), Organization: organization, Service: service,
		Environment: environment, Name: name, ValueDigest: hex.EncodeToString(digest.Sum(nil)),
		KeyVersion: config.SecretsActiveKey, CreatedBy: actor, Created: now(),
	}
	ciphertext := aead.Seal(nil, nonce, []byte(value), workerSecretAAD(version))
	return encryptedWorkerSecret{Version: version, Ciphertext: ciphertext, Nonce: nonce}, nil
}

func decryptWorkerSecret(config *WorkersConfig, encrypted encryptedWorkerSecret) (string, error) {
	key, err := config.workerSecretKey(encrypted.Version.KeyVersion)
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
	plaintext, err := aead.Open(nil, encrypted.Nonce, encrypted.Ciphertext, workerSecretAAD(encrypted.Version))
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

func (s *Server) workerSecretList(st *State, p Principal, r *http.Request) (any, int, error) {
	environment := r.PathValue("environment")
	if !workerEnvValid(environment) {
		return nil, 0, notFound()
	}
	target, err := s.workerTarget(st, r, environment, "service.read", p)
	if err != nil {
		return nil, 0, err
	}
	service := s.workerService(st, target)
	current, ok := service.Environments[environment]
	if !ok {
		return nil, 0, notFound()
	}
	summaries := make([]workerSecretSummary, 0, len(current.SecretVersions))
	for name, versionID := range current.SecretVersions {
		version, ok := st.Workers.SecretVersions[versionID]
		if !ok || version.Service != target.ID || version.Environment != environment || version.Name != name {
			return nil, 0, errors.New("Worker secret metadata is inconsistent")
		}
		summaries = append(summaries, workerSecretSummary{Name: name, CreatedAt: version.Created})
	}
	sort.Slice(summaries, func(i, j int) bool { return summaries[i].Name < summaries[j].Name })
	return items(summaries)
}

func (s *Server) workerSecretPut(st *State, p Principal, r *http.Request) (any, int, error) {
	return s.workerSecretChange(st, p, r, false)
}

func (s *Server) workerSecretDelete(st *State, p Principal, r *http.Request) (any, int, error) {
	return s.workerSecretChange(st, p, r, true)
}

// workerSecretChange mirrors Cloudflare's secret put/delete behavior: create
// an immutable Worker version and immediately deploy it. The current Artifact
// is reused, so rotating a secret never rebuilds user code.
func (s *Server) workerSecretChange(st *State, p Principal, r *http.Request, deleting bool) (any, int, error) {
	environment, name := r.PathValue("environment"), r.PathValue("secret")
	if !workerEnvValid(environment) || !workerSecretNameValid(name) {
		return nil, 0, notFound()
	}
	target, err := s.workerTarget(st, r, environment, "service.publish", p)
	if err != nil {
		return nil, 0, err
	}
	var input workerSecretInput
	if err = body(r, &input); err != nil {
		return nil, 0, err
	}
	if input.Key == "" || len(input.Key) > 128 || input.Revision < 0 {
		return nil, 0, problem(422, "WORKER_SECRET_INVALID", "expected_revision and idempotency_key required")
	}
	if !deleting && (len(input.Value) == 0 || len(input.Value) > workerSecretMaxBytes) {
		return nil, 0, problem(422, "WORKER_SECRET_INVALID", "Secret value must contain 1–16384 bytes")
	}
	kind := "secret-put:" + name
	if deleting {
		kind = "secret-delete:" + name
	}
	idempotencyKey := workerIdempotencyKey(target, p, kind, environment, input.Key)
	operationID, err := workerIdempotent(st, idempotencyKey, input)
	if err != nil {
		return nil, 0, err
	}
	if operationID != "" {
		return st.Workers.Operations[operationID], 200, nil
	}
	service := s.workerService(st, target)
	current, ok := service.Environments[environment]
	if !ok {
		return nil, 0, notFound()
	}
	if !current.Enabled {
		return nil, 0, problem(409, "ENVIRONMENT_DISABLED", "Target environment is disabled")
	}
	if current.Revision != input.Revision {
		return nil, 0, problem(409, "REVISION_CONFLICT", "Environment release changed")
	}
	if workerBusy(st, target.ID, environment) {
		return nil, 0, problem(409, "RELEASE_BUSY", "Wait for the active release operation")
	}
	active, ok := st.Workers.Releases[current.Release]
	if !ok || current.Release == "" || active.Artifact == "" {
		return nil, 0, problem(409, "ENVIRONMENT_NOT_PUBLISHED", "Publish the Worker before configuring secrets")
	}
	if _, collision := current.Variables[name]; collision && !deleting {
		return nil, 0, problem(409, "WORKER_BINDING_CONFLICT", "A plain variable already uses this binding name")
	}
	versions := cloneStringsMap(current.SecretVersions)
	if deleting {
		if _, exists := versions[name]; !exists {
			return nil, 0, notFound()
		}
		delete(versions, name)
	} else {
		if len(versions) >= workerSecretMaxCount {
			if _, exists := versions[name]; !exists {
				return nil, 0, problem(422, "WORKER_SECRET_INVALID", "At most 32 secrets are supported per environment")
			}
		}
		encrypted, encryptErr := encryptWorkerSecret(s.Config.Workers, target.Organization, target.ID, environment, name, p.User.ID, input.Value)
		if encryptErr != nil {
			return nil, 0, problem(503, "WORKER_SECRETS_NOT_CONFIGURED", "Worker secret encryption is not configured")
		}
		st.Workers.SecretVersions[encrypted.Version.ID] = encrypted.Version
		if st.PendingWorkerSecrets == nil {
			st.PendingWorkerSecrets = map[string]encryptedWorkerSecret{}
		}
		st.PendingWorkerSecrets[encrypted.Version.ID] = encrypted
		versions[name] = encrypted.Version.ID
	}

	current.SecretVersions = versions
	current.SecretNames = workerSecretNames(versions)
	current.ConfigRevision++
	service.Environments[environment] = current
	st.Workers.Services[target.ID] = service

	release := WorkerRelease{
		Service: target.ID, Organization: target.Organization, Environment: environment,
		Artifact: active.Artifact, Profile: active.Profile, ArchiveDigest: active.ArchiveDigest, ModuleDigest: active.ModuleDigest,
		CompatibilityDate: active.CompatibilityDate, CompatibilityFlags: append([]string(nil), active.CompatibilityFlags...),
		ConfigRevision: current.ConfigRevision, Variables: cloneStringsMap(current.Variables),
		SecretDigest: workerSecretsDigest(versions), SecretVersions: cloneStringsMap(versions),
		Created: now(),
	}
	identity := release
	identity.Created = time.Time{}
	raw, _ := json.Marshal(identity)
	release.ID = "rel_" + bytesDigest(raw)
	if existing, exists := st.Workers.Releases[release.ID]; exists {
		release = existing
	} else {
		st.Workers.Releases[release.ID] = release
	}

	operationID = randomID("op")
	operation := WorkerOperation{
		ID: operationID, Kind: "secret", Organization: target.Organization, Service: target.ID,
		Environment: environment, State: "queued", Release: release.ID,
		ExpectedRevision: input.Revision, ConfigRevision: current.ConfigRevision,
		Steps: []WorkerStep{{Name: "artifact", State: "pending"}, {Name: "candidate", State: "pending"}, {Name: "commit", State: "pending"}, {Name: "synchronize", State: "pending"}}, Created: now(),
	}
	st.Workers.Operations[operationID] = operation
	st.Workers.OperationPrivate[operationID] = WorkerOperationPrivate{Actor: workerActor(p)}
	workerRemember(st, idempotencyKey, input, operationID)
	action := "workers.secret.put"
	if deleting {
		action = "workers.secret.delete"
	}
	s.audit(st, p, r, action, target.ID+"/"+environment+"/"+name, true)
	return operation, 202, nil
}
