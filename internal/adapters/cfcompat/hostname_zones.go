package cfcompat

import (
	"net/http"
	"strings"

	"github.com/krun-dev/krun/internal/authorization"
	"github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/securetoken"
	"github.com/krun-dev/krun/internal/storage"
)

type hostnameZone struct {
	ID      string              `json:"id"`
	Name    string              `json:"name"`
	Status  string              `json:"status"`
	Account map[string]string   `json:"account"`
	Suffix  config.WorkerDomain `json:"-"`
}

func zoneID(org, suffix string) string {
	return securetoken.Digest(org + "\x00" + suffix)[:32]
}

func (s *Server) hostnameZones(st *domain.State, p authorization.Principal, r *http.Request) ([]hostnameZone, error) {
	filter := r.URL.Query().Get("account.id")
	if account := r.PathValue("org"); account != "" {
		filter = account
	} else if filter != "" {
		// Wrangler accepts either an organization ID or its short name as
		// account_id and sends that value during Zone discovery as well.
		lookup := r.Clone(r.Context())
		lookup.SetPathValue("account", filter)
		if err := s.ResolveAccount(lookup); err != nil {
			return nil, err
		}
		filter = lookup.PathValue("org")
	}
	name := strings.ToLower(r.URL.Query().Get("name"))
	out := []hostnameZone{}
	for _, org := range st.Organizations {
		if filter != "" && filter != org.ID {
			continue
		}
		target, err := authorization.Resolve(st, "organization", org.ID, "global")
		if err != nil {
			return nil, err
		}
		read, err := authorization.Authorize(st, p, "worker.hostname.read", target)
		if err != nil {
			return nil, err
		}
		if !read.Allowed {
			continue
		}
		for _, suffix := range s.Config.Workers.Domains {
			if name != "" && name != suffix.Domain {
				continue
			}
			if !suffix.Allows(config.WorkerDomainCustom) && !suffix.Allows(config.WorkerDomainApplication) && r.PathValue("zone") == "" {
				continue
			}
			out = append(out, hostnameZone{zoneID(org.ID, suffix.ID), suffix.Domain, "active", map[string]string{"id": org.ID, "name": org.Name}, suffix})
		}
	}
	return out, nil
}

func (s *Server) cfHostnameZones(st *domain.State, p authorization.Principal, r *http.Request) (any, int, error) {
	zones, err := s.hostnameZones(st, p, r)
	return zones, http.StatusOK, err
}

func (s *Server) cfHostnameZone(st *domain.State, p authorization.Principal, r *http.Request) (hostnameZone, error) {
	zones, err := s.hostnameZones(st, p, r)
	if err != nil {
		return hostnameZone{}, err
	}
	for _, zone := range zones {
		if zone.ID == r.PathValue("zone") {
			r.SetPathValue("org", zone.Account["id"])
			return zone, nil
		}
	}
	return hostnameZone{}, domain.NotFound()
}

func (s *Server) cfGetHostnameZone(st *domain.State, p authorization.Principal, r *http.Request) (any, int, error) {
	zone, err := s.cfHostnameZone(st, p, r)
	return zone, http.StatusOK, err
}

func (s *Server) withHostnameTargets(r *http.Request, p authorization.Principal, fn func(*domain.State) (any, int, error)) (any, int, error) {
	key := ""
	if p.Key != nil {
		key = p.Key.ID
	}
	ctx := storage.WithReadScope(r.Context(), storage.ReadScope{
		User: p.User.ID, Key: key, Organization: r.PathValue("org"), Kind: "worker",
		Summary: true, WorkerHostnameTargets: true,
	})
	var out any
	var status int
	err := s.Store.Mutate(ctx, storage.PartitionAll, 0, func(st *domain.State) error {
		var err error
		out, status, err = fn(st)
		return err
	})
	return out, status, err
}
