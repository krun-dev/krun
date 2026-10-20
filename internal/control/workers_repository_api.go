package control

import (
	"net/http"
	"sort"
)

type workerServiceSummary struct {
	Resource
	Repository   *WorkerRepositorySource    `json:"repository_source,omitempty"`
	Environments []WorkerEnvironmentSummary `json:"environments"`
}

type WorkerEnvironmentSummary struct {
	Name       string `json:"name"`
	Enabled    bool   `json:"enabled"`
	Deployment string `json:"deployment_id,omitempty"`
	Confirmed  string `json:"confirmed_deployment_id,omitempty"`
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
				summary.Environments = append(summary.Environments, WorkerEnvironmentSummary{env, current.Enabled, current.Deployment, current.Confirmed})
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
	return items(s.visibleRepositoryProviders(st, target.ID, p.User.ID))
}

func (s *Server) workerRepositoryGet(st *State, p Principal, r *http.Request) (any, int, error) {
	target, err := s.workerCollectionTarget(st, r, "service.read", p)
	if err != nil {
		return nil, 0, err
	}
	service := s.workerService(st, target)
	return map[string]any{"source": service.Repository, "revision": service.RepositoryRevision}, 200, nil
}

func (s *Server) workerRepositoryUpdate(st *State, p Principal, r *http.Request) (any, int, error) {
	// Repository settings are shared by all environments of a service.
	target, err := s.workerTarget(st, r, "global", "service.update", p)
	if err != nil {
		return nil, 0, err
	}
	var in struct {
		Source   WorkerRepositorySource `json:"source"`
		Revision int                    `json:"expected_revision"`
	}
	if err := body(r, &in); err != nil {
		return nil, 0, err
	}
	if _, err := s.validateRepositorySource(&in.Source, target.Organization); err != nil {
		return nil, 0, err
	}
	service := s.workerService(st, target)
	if service.RepositoryRevision != in.Revision {
		return nil, 0, problem(409, "REVISION_CONFLICT", "Repository configuration changed")
	}
	service.Repository = &in.Source
	service.RepositoryRevision++
	st.Workers.Services[target.ID] = service
	return map[string]any{"source": service.Repository, "revision": service.RepositoryRevision}, 200, nil
}
