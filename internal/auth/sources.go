package auth

import (
	"context"
	"errors"

	appconfig "github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/storage"
)

func (s *Service) RegisterSources(ctx context.Context, providers []appconfig.ProviderConfig) error {
	registrationCtx := ctx
	registrationCtx = storage.WithReadScope(ctx, storage.ReadScope{ActorOnly: true})
	if e := s.Store.Update(registrationCtx, storage.PartitionIAM, func(st *domain.State) error {
		for _, p := range providers {
			fp := appconfig.Fingerprint(p)
			if old, ok := st.Sources[p.Source]; ok && old != fp {
				return errors.New("existing identity source namespace cannot be replaced")
			}
			providerKey := "provider:" + p.ID
			namespaceKey := "namespace:" + appconfig.Namespace(p)
			if old, ok := st.Sources[providerKey]; ok && old != p.Source {
				return errors.New("provider identity source cannot be reassigned")
			}
			if old, ok := st.Sources[namespaceKey]; ok && old != p.Source {
				return errors.New("identity namespace already registered")
			}
			st.Sources[providerKey] = p.Source
			st.Sources[namespaceKey] = p.Source
			st.Sources[p.Source] = fp
		}
		return nil
	}); e != nil {
		return e
	}

	return nil
}
