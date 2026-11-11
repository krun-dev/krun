package repository

import (
	"context"
	"errors"
	"slices"
	"time"

	repooauth "github.com/krun-dev/krun/internal/adapters/repositoryoauth"
	appconfig "github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/domain"
)

type CatalogPage struct {
	Items   []repooauth.Repository `json:"items"`
	HasMore bool                   `json:"has_more"`
}

// Catalog never uses a shared credential or another user's connection.
func (s *Service) Catalog(ctx context.Context, provider appconfig.WorkerRepositoryProvider, user string, page int) (CatalogPage, error) {
	grant, ok, err := s.userRepositoryGrant(ctx, provider, user)
	if err != nil {
		return CatalogPage{}, err
	}
	if !ok || provider.ClientID == "" {
		return CatalogPage{}, reconnectRequired()
	}
	items, more, err := (&repooauth.Client{Config: s.Config}).Repositories(ctx, provider, grant.AccessToken, page)
	switch {
	case errors.Is(err, repooauth.ErrCatalogUnauthorized):
		if err := s.rejectGrant(ctx, grant); err != nil {
			return CatalogPage{}, err
		}
		return CatalogPage{}, reconnectRequired()
	case errors.Is(err, repooauth.ErrCatalogForbidden):
		return CatalogPage{}, domain.ForbiddenCode("WORKER_REPOSITORY_LIST_DENIED", "Repository listing was denied; check the Git provider permissions or reconnect")
	case err != nil:
		return CatalogPage{}, domain.Unavailable("WORKER_REPOSITORY_LIST_UNAVAILABLE", "Repository listing is temporarily unavailable; retry")
	}
	if len(provider.AllowedRepositories) > 0 {
		items = slices.DeleteFunc(items, func(item repooauth.Repository) bool {
			return !slices.Contains(provider.AllowedRepositories, item.Name)
		})
	}
	return CatalogPage{Items: items, HasMore: more && page < 1000}, nil
}

func (s *Service) rejectGrant(ctx context.Context, grant domain.WorkerRepositoryConnection) error {
	return s.Store.RejectRepositoryConnection(ctx, grant)
}

// CheckBindingAuthorization runs while saving binding metadata or enqueueing a
// build. The controller refreshes an expired, renewable grant when fetching.
func (s *Service) CheckBindingAuthorization(ctx context.Context, provider appconfig.WorkerRepositoryProvider, user string) error {
	if provider.ClientID == "" {
		return reconnectRequired()
	}
	grant, ok, err := s.Store.RepositoryConnection(ctx, user, authorizationKey(provider))
	if err != nil {
		return err
	}
	if !ok || grant.Status(time.Now().UTC()) == "reconnect_required" {
		return reconnectRequired()
	}
	return nil
}
