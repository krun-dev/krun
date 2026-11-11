package workers

import (
	"context"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/krun-dev/krun/internal/authorization"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/securetoken"
)

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
	if s.Config.Workers == nil || s.Config.Workers.SecretsActiveKey == "" {
		return domain.Unavailable("HYPERDRIVE_NOT_CONFIGURED", "Hyperdrive requires worker secret encryption")
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

// HyperdriveConnectionStrings issues source credentials only for bindings on
// a version currently served by the authenticated pool.
func (s *Service) HyperdriveConnectionStrings(ctx context.Context, versionID, poolID string) (map[string]string, error) {
	origins, err := s.Store.RuntimeHyperdriveOrigins(ctx, versionID, poolID)
	if err != nil {
		return nil, err
	}
	result := make(map[string]string, len(origins))
	for binding, origin := range origins {
		password, err := DecryptWorkerSecret(s.Config.Workers, origin.Secret)
		if err != nil {
			return nil, err
		}
		dsn := url.URL{
			Scheme:   "postgresql",
			User:     url.UserPassword(origin.User, password),
			Host:     net.JoinHostPort(origin.Host, strconv.Itoa(origin.Port)),
			Path:     "/" + origin.Database,
			RawPath:  "/" + url.PathEscape(origin.Database),
			RawQuery: "sslmode=verify-full",
		}
		result[binding] = dsn.String()
	}
	return result, nil
}

func (s *Service) validateHyperdriveBindings(ctx context.Context, org string, bindings map[string]string) error {
	if len(bindings) == 0 {
		return nil
	}
	if s.Config.Workers == nil || s.Config.Workers.SecretsActiveKey == "" {
		return domain.Unavailable("HYPERDRIVE_NOT_CONFIGURED", "Hyperdrive requires worker secret encryption")
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
