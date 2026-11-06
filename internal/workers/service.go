package workers

import (
	"context"

	"github.com/krun-dev/krun/internal/authorization"
	appconfig "github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/repository"
	"github.com/krun-dev/krun/internal/storage"
)

// WorkerRef identifies an organization worker and, when applicable, one of
// its environments or subordinate items. Item is a build, version, operation,
// artifact or secret name, as specified by the called operation.
type WorkerRef struct{ Organization, Worker, Environment, Item string }

// Outcome describes an application result; HTTP status mapping belongs to the adapter.
type Outcome uint8

const (
	Read Outcome = iota
	Created
	Queued
	Deleted
)

type Service struct {
	Config     *appconfig.Config
	Store      *storage.Store
	Controller *Controller
	Repository *repository.Service
	Require    func(*domain.State, authorization.Principal, string, authorization.Target) error
	Audit      func(*domain.State, authorization.Principal, string, string, bool)
}

func (s *Service) require(st *domain.State, p authorization.Principal, action string, target authorization.Target) error {
	if s.Require != nil {
		return s.Require(st, p, action, target)
	}
	decision, err := authorization.Authorize(st, p, action, target)
	if err != nil {
		return err
	}
	if !decision.Allowed {
		return domain.Forbidden()
	}
	return nil
}

func (s *Service) audit(st *domain.State, p authorization.Principal, action, target string, allowed bool) {
	if s.Audit != nil {
		s.Audit(st, p, action, target, allowed)
	}
}

func (s *Service) orgTarget(st *domain.State, ref WorkerRef) (authorization.Target, error) {
	return authorization.Resolve(st, "organization", ref.Organization, "global")
}

func (s *Service) sourceEnabled(source string) bool {
	for _, provider := range s.Config.Providers {
		if provider.Source == source && provider.IsEnabled() {
			return true
		}
	}
	return false
}

func collection(items any) (any, Outcome, error) { return map[string]any{"items": items}, Read, nil }

var _ context.Context
