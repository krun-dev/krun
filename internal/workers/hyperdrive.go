package workers

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/krun-dev/krun/internal/authorization"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/securetoken"
)

const HyperdriveUser = "krun"

var hyperdriveOriginHost = regexp.MustCompile(`(?i)^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$`)

type HyperdriveInput struct {
	Name   string `json:"name,omitempty"`
	Origin *struct {
		Scheme   string  `json:"scheme,omitempty"`
		Host     string  `json:"host,omitempty"`
		Port     int     `json:"port,omitempty"`
		Database string  `json:"database,omitempty"`
		User     string  `json:"user,omitempty"`
		Password *string `json:"password,omitempty"`
	} `json:"origin,omitempty"`
	Caching *struct {
		Disabled bool `json:"disabled,omitempty"`
	} `json:"caching,omitempty"`
}

func (s *Service) AuthorizeHyperdrive(st *domain.State, p authorization.Principal, org string, write bool) error {
	if s.Config.Workers == nil || s.Config.Workers.Hyperdrive == nil {
		return domain.Unavailable("HYPERDRIVE_NOT_CONFIGURED", "Hyperdrive proxy is not configured on this control plane; workers.hyperdrive is required in the server config (see config/example.yaml)")
	}
	target, err := s.orgTarget(st, WorkerRef{Organization: org})
	if err != nil {
		return err
	}
	action := "worker.list"
	if write {
		action = "worker.create"
	}
	return s.require(st, p, action, target)
}

func (s *Service) SaveHyperdrive(ctx context.Context, org, actor, id string, input HyperdriveInput) (domain.HyperdriveConfig, error) {
	item := domain.HyperdriveOrigin{}
	if input.Caching != nil && !input.Caching.Disabled {
		return item.HyperdriveConfig, domain.Invalid("HYPERDRIVE_UNSUPPORTED", "Hyperdrive query caching is unavailable")
	}
	if id != "" {
		var err error
		item.HyperdriveConfig, err = s.Store.GetHyperdrive(ctx, org, id)
		if err != nil {
			return item.HyperdriveConfig, err
		}
	} else {
		item.ID = strings.ReplaceAll(securetoken.UUID(), "-", "")
		item.Organization = org
		item.Revision = 1
		item.Created = time.Now().UTC()
	}
	if input.Name != "" {
		item.Name = strings.TrimSpace(input.Name)
	}
	if item.Name == "" || len(item.Name) > 100 {
		return item.HyperdriveConfig, domain.Invalid("HYPERDRIVE_INVALID", "Hyperdrive name must contain 1–100 bytes")
	}
	if origin := input.Origin; origin != nil {
		if origin.Scheme != "" && origin.Scheme != "postgresql" && origin.Scheme != "postgres" {
			return item.HyperdriveConfig, domain.Invalid("HYPERDRIVE_UNSUPPORTED", "Only PostgreSQL is supported")
		}
		host := strings.ToLower(strings.TrimSuffix(origin.Host, "."))
		if id == "" {
			item.Host, item.Port, item.Database, item.User = host, origin.Port, origin.Database, origin.User
			if item.Port == 0 {
				item.Port = 5432
			}
		} else if (host != "" && host != item.Host) || (origin.Port != 0 && origin.Port != item.Port) ||
			(origin.Database != "" && origin.Database != item.Database) || (origin.User != "" && origin.User != item.User) {
			return item.HyperdriveConfig, domain.Invalid("HYPERDRIVE_ORIGIN_IMMUTABLE", "Create a new configuration to change the origin")
		}
		if origin.Password != nil {
			if !hyperdriveOriginValue(*origin.Password, 4096) {
				return item.HyperdriveConfig, domain.Invalid("HYPERDRIVE_INVALID", "A valid origin password is required")
			}
			var err error
			item.Secret, err = EncryptWorkerSecret(s.Config.Workers, org, item.ID, "hyperdrive", "password", actor, *origin.Password)
			if err != nil {
				return item.HyperdriveConfig, domain.Unavailable("HYPERDRIVE_NOT_CONFIGURED", "Hyperdrive secret encryption is unavailable")
			}
		}
	}
	if id != "" {
		return item.HyperdriveConfig, s.Store.UpdateHyperdrive(ctx, item)
	}
	_, ipErr := netip.ParseAddr(item.Host)
	if ipErr == nil || len(item.Host) > 253 || !hyperdriveOriginHost.MatchString(item.Host) ||
		item.Port < 1 || item.Port > 65535 || !hyperdriveOriginValue(item.Database, 63) ||
		!hyperdriveOriginValue(item.User, 63) || item.Secret.Version.ID == "" {
		return item.HyperdriveConfig, domain.Invalid("HYPERDRIVE_INVALID", "A PostgreSQL hostname, port, database, username and password are required")
	}
	return item.HyperdriveConfig, s.Store.CreateHyperdrive(ctx, item)
}

func hyperdriveOriginValue(value string, limit int) bool {
	return len(value) > 0 && len(value) <= limit && !strings.ContainsRune(value, 0)
}

func (s *Service) HyperdriveConnectionStrings(version domain.WorkerVersion) (map[string]string, error) {
	workers := s.Config.Workers
	if workers == nil || workers.Hyperdrive == nil {
		return nil, domain.Unavailable("HYPERDRIVE_NOT_CONFIGURED", "Hyperdrive proxy is not configured on this control plane; workers.hyperdrive is required in the server config (see config/example.yaml)")
	}
	key, err := workers.WorkerSecretKey(workers.SecretsActiveKey)
	if err != nil {
		return nil, err
	}
	keyID := base64.RawURLEncoding.EncodeToString([]byte(workers.SecretsActiveKey))
	result := make(map[string]string, len(version.HyperdriveBindings))
	for binding := range version.HyperdriveBindings {
		claims := keyID + "." + version.ID + "." + binding
		token := claims + "." + base64.RawURLEncoding.EncodeToString(hyperdriveMAC(key, claims))
		dsn := url.URL{
			Scheme: "postgresql",
			User:   url.UserPassword(HyperdriveUser, token),
			Host:   workers.Hyperdrive.Address,
			Path:   "/krun",
		}
		dsn.RawQuery = "sslmode=verify-full"
		result[binding] = dsn.String()
	}
	return result, nil
}

func (s *Service) ResolveHyperdriveSession(ctx context.Context, user, token string) (domain.HyperdriveConfig, string, bool, bool, error) {
	workers := s.Config.Workers
	if workers == nil || workers.Hyperdrive == nil || user != HyperdriveUser || len(token) > 512 {
		return domain.HyperdriveConfig{}, "", false, false, domain.Forbidden()
	}
	claims, encodedMAC, ok := strings.Cut(token, ".")
	if !ok {
		return domain.HyperdriveConfig{}, "", false, false, domain.Forbidden()
	}
	keyID, err := base64.RawURLEncoding.DecodeString(claims)
	key, keyErr := workers.WorkerSecretKey(string(keyID))
	parts := strings.Split(encodedMAC, ".")
	if err != nil || keyErr != nil || len(parts) != 3 {
		return domain.HyperdriveConfig{}, "", false, false, domain.Forbidden()
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	signed := token[:len(token)-len(parts[2])-1]
	if err != nil || !hmac.Equal(hyperdriveMAC(key, signed), signature) {
		return domain.HyperdriveConfig{}, "", false, false, domain.Forbidden()
	}
	origin, poolID, err := s.Store.HyperdriveForSession(ctx, parts[0], parts[1])
	if err != nil {
		return domain.HyperdriveConfig{}, "", false, false, err
	}
	pool, ok := workers.RuntimePool(poolID)
	if !ok || pool.PublicEgress != "direct" && pool.PrivateEgress != "direct" {
		return domain.HyperdriveConfig{}, "", false, false, domain.Forbidden()
	}
	password, err := DecryptWorkerSecret(workers, origin.Secret)
	return origin.HyperdriveConfig, password, pool.PublicEgress == "direct", pool.PrivateEgress == "direct", err
}

func (s *Service) ResolveHyperdriveOrigin(ctx context.Context, org, id string) (domain.HyperdriveConfig, string, error) {
	origin, err := s.Store.GetHyperdriveOrigin(ctx, org, id)
	if err != nil {
		return domain.HyperdriveConfig{}, "", err
	}
	password, err := DecryptWorkerSecret(s.Config.Workers, origin.Secret)
	return origin.HyperdriveConfig, password, err
}

func hyperdriveMAC(key []byte, claims string) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("krun-hyperdrive-binding-v1\x00" + claims))
	return mac.Sum(nil)
}

func (s *Service) validateHyperdriveBindings(ctx context.Context, org string, bindings map[string]string) error {
	if len(bindings) == 0 {
		return nil
	}
	if s.Config.Workers == nil || s.Config.Workers.Hyperdrive == nil {
		return domain.Unavailable("HYPERDRIVE_NOT_CONFIGURED", "Hyperdrive proxy is not configured on this control plane; workers.hyperdrive is required in the server config (see config/example.yaml)")
	}
	if len(bindings) > 32 {
		return domain.Invalid("WORKER_BINDINGS_INVALID", "Too many Hyperdrive bindings")
	}
	for _, id := range bindings {
		if _, err := s.Store.GetHyperdrive(ctx, org, id); err != nil {
			return err
		}
	}
	return nil
}
