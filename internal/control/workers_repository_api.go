package control

import (
	"net/http"
	"sort"
	"strings"
)

type workerServiceSummary struct {
	Resource
	Repository   *WorkerRepositorySource    `json:"repository_source,omitempty"`
	Environments []WorkerEnvironmentSummary `json:"environments"`
}

type WorkerEnvironmentSummary struct {
	Name      string   `json:"name"`
	Domains   []string `json:"domains,omitempty"`
	Hosts     []string `json:"hosts,omitempty"`
	Enabled   bool     `json:"enabled"`
	Release   string   `json:"release_id,omitempty"`
	Confirmed string   `json:"confirmed_release_id,omitempty"`
}

// WorkerDomainView is the client-facing domain catalog entry. It deliberately
// omits Gateway and pool identifiers: organization users choose only the
// portable public/private access primitive.
type WorkerDomainView struct {
	ID           string `json:"id"`
	Domain       string `json:"domain"`
	Label        string `json:"label"`
	Access       string `json:"access"`
	StaticDomain string `json:"static_domain"`
	Default      bool   `json:"default"`
}

type WorkerNetworkOptionView struct {
	ID      string   `json:"id"`
	Access  []string `json:"access"`
	Default bool     `json:"default"`
}

func (s *Server) workerNetworkOptions(st *State, p Principal, r *http.Request) (any, int, error) {
	target, err := s.orgTarget(st, r)
	if err != nil {
		return nil, 0, err
	}
	if err := s.require(st, p, r, "service.list", target); err != nil {
		return nil, 0, err
	}
	seen := map[string]bool{}
	out := []WorkerNetworkOptionView{}
	pools := s.Config.Workers.configuredRuntimePools()
	if len(s.Config.Workers.RuntimePools) == 0 {
		available := pools[0].Access
		defaultAccessRaw := []string{}
		for _, id := range s.Config.Workers.defaultDomains() {
			if domain, ok := s.Config.Workers.domain(id); ok {
				defaultAccessRaw = append(defaultAccessRaw, domain.Access)
			}
		}
		defaultAccess, _ := normalizeWorkerAccess(defaultAccessRaw)
		pools = nil
		for _, access := range available {
			pools = append(pools, WorkerRuntimePool{Access: []string{access}, Default: sameWorkerAccess([]string{access}, defaultAccess)})
		}
		if len(available) > 1 {
			pools = append(pools, WorkerRuntimePool{Access: append([]string(nil), available...), Default: sameWorkerAccess(available, defaultAccess)})
		}
	}
	for _, pool := range pools {
		id := strings.Join(pool.Access, "+")
		if seen[id] {
			continue
		}
		available := true
		for _, access := range pool.Access {
			found := false
			for _, domain := range s.Config.Workers.Domains {
				if domain.Access == access {
					found = true
				}
			}
			available = available && found
		}
		if available {
			seen[id] = true
			out = append(out, WorkerNetworkOptionView{ID: id, Access: append([]string(nil), pool.Access...), Default: pool.Default})
		}
	}
	return items(out)
}

// domainCatalog renders the platform domain catalog for clients.
func (s *Server) domainCatalog() []WorkerDomainView {
	out := make([]WorkerDomainView, 0, len(s.Config.Workers.Domains))
	for _, d := range s.Config.Workers.Domains {
		out = append(out, WorkerDomainView{ID: d.ID, Domain: d.Domain, Label: d.Label, Access: d.Access, StaticDomain: d.StaticDomain, Default: d.Default})
	}
	return out
}

// workerDomains lists the domain catalog so the console can offer choices.
func (s *Server) workerDomains(st *State, p Principal, r *http.Request) (any, int, error) {
	target, err := s.orgTarget(st, r)
	if err != nil {
		return nil, 0, err
	}
	if err := s.require(st, p, r, "service.list", target); err != nil {
		return nil, 0, err
	}
	return items(s.domainCatalog())
}

func (s *Server) workerServiceList(st *State, p Principal, r *http.Request) (any, int, error) {
	target, err := s.orgTarget(st, r)
	if err != nil {
		return nil, 0, err
	}
	if err := s.require(st, p, r, "service.list", target); err != nil {
		return nil, 0, err
	}
	out := make([]workerServiceSummary, 0)
	for _, resource := range st.Resources {
		if resource.Kind != "service" || resource.Organization != target.ID {
			continue
		}
		summary := workerServiceSummary{Resource: resource, Environments: []WorkerEnvironmentSummary{}}
		service := st.Workers.Services[resource.ID]
		names := make([]string, 0, len(service.Environments)+1)
		if _, exists := service.Environments[workerBaseline]; !exists {
			names = append(names, workerBaseline)
		}
		for name := range service.Environments {
			names = append(names, name)
		}
		sort.Slice(names, func(i, j int) bool {
			if names[i] == workerBaseline {
				return true
			}
			if names[j] == workerBaseline {
				return false
			}
			return names[i] < names[j]
		})
		visible := false
		for _, env := range names {
			decision, err := Authorize(st, p, "service.read", Target{Kind: "service", ID: resource.ID, Organization: target.ID, Environment: env})
			if err != nil {
				return nil, 0, err
			}
			if !decision.Allowed {
				continue
			}
			visible = true
			if current, exists := service.Environments[env]; exists {
				// Hosts are derived for display; the persisted row is not rewritten
				// from a read path.
				s.syncEnvironmentHosts(&current, resource.Organization, resource.ID, s.syncServiceDomains(service))
				summary.Environments = append(summary.Environments, WorkerEnvironmentSummary{Name: env, Domains: current.Domains, Hosts: current.Hosts, Enabled: current.Enabled, Release: current.Release, Confirmed: current.Confirmed})
			}
		}
		if visible {
			summary.Repository = service.Repository
			out = append(out, summary)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name == out[j].Name {
			return out[i].ID < out[j].ID
		}
		return out[i].Name < out[j].Name
	})
	return items(out)
}

func (s *Server) workerRepositoryProviders(st *State, p Principal, r *http.Request) (any, int, error) {
	target, err := s.orgTarget(st, r)
	if err != nil {
		return nil, 0, err
	}
	if err := s.require(st, p, r, "service.list", target); err != nil {
		return nil, 0, err
	}
	connected, err := s.Store.connectedRepositoryProviders(r.Context(), p.User.ID)
	if err != nil {
		return nil, 0, err
	}
	return items(s.visibleRepositoryProviders(target.ID, connected))
}

func (s *Server) workerRepositoryGet(st *State, p Principal, r *http.Request) (any, int, error) {
	target, err := s.workerCollectionTarget(st, r, "service.read", p)
	if err != nil {
		return nil, 0, err
	}
	service := s.workerServiceView(st, target)
	return map[string]any{"source": service.Repository, "revision": service.RepositoryRevision}, 200, nil
}

func (s *Server) workerRepositoryUpdate(st *State, p Principal, r *http.Request) (any, int, error) {
	// Repository settings are shared by all environments of a service.
	target, err := s.workerTarget(st, r, "global", "service.update", p)
	if err != nil {
		return nil, 0, err
	}
	var in struct {
		Source   *WorkerRepositorySource `json:"source"`
		Revision int                     `json:"expected_revision"`
	}
	if err := body(r, &in); err != nil {
		return nil, 0, err
	}
	service := s.workerService(st, target)
	if service.RepositoryRevision != in.Revision {
		return nil, 0, problem(409, "REVISION_CONFLICT", "Repository configuration changed")
	}
	if in.Source == nil {
		service.Repository = nil
	} else {
		if _, err := s.validateRepositorySource(in.Source, target.Organization); err != nil {
			return nil, 0, err
		}
		service.Repository = in.Source
	}
	service.RepositoryRevision++
	st.Workers.Services[target.ID] = service
	return map[string]any{"source": service.Repository, "revision": service.RepositoryRevision}, 200, nil
}
