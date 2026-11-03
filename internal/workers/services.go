package workers

import (
	context "context"
	"sort"
	"time"

	authorization "github.com/krun-dev/krun/internal/authorization"
	appconfig "github.com/krun-dev/krun/internal/config"
	domain "github.com/krun-dev/krun/internal/domain"
)

type WorkerServiceDomainsUpdateInput struct {
	Domains          []string `json:"domains"`
	ExpectedRevision *int     `json:"expected_revision"`
}

type WorkerServiceNetworkProfileUpdateInput struct {
	Profile          string `json:"network_profile"`
	ExpectedRevision *int   `json:"expected_revision"`
}

type WorkerRepositoryUpdateInput struct {
	Source   *domain.WorkerRepositorySource `json:"source"`
	Revision int                            `json:"expected_revision"`
}

func (s *Service) WorkerService(st *domain.State, t authorization.Target) domain.WorkerService {
	if st.Workers == nil {
		st.Workers = domain.NewWorkerState()
	}
	ws, ok := st.Workers.Services[t.ID]
	if !ok {
		ws = domain.WorkerService{Service: t.ID, Organization: t.Organization, Domains: []string{}, Environments: map[string]domain.WorkerEnvironment{}}
	}
	ws.Organization = t.Organization
	ws = s.NormalizeService(ws, st)
	st.Workers.Services[t.ID] = ws
	s.EnsureWorkerPlacement(st, ws)
	return ws
}

func (s *Service) EnsureWorkerPlacement(st *domain.State, ws domain.WorkerService) domain.WorkerPlacement {
	if placement, ok := domain.ActiveWorkerPlacement(st.Workers, ws.Service); ok &&
		placement.NetworkProfile == ws.NetworkProfile && s.Config.Workers.PoolAllowedForProfile(ws.NetworkProfile, placement.Pool) {
		return placement
	}
	pool, ok := s.Config.Workers.SelectPoolForService(ws.NetworkProfile, ws.Service)
	if !ok {
		return domain.WorkerPlacement{}
	}
	return s.ReplaceWorkerPlacement(st, ws, pool.ID)
}

func (s *Service) ReplaceWorkerPlacement(st *domain.State, ws domain.WorkerService, pool string) domain.WorkerPlacement {
	for key, placement := range st.Workers.Placements {
		if placement.Service == ws.Service && placement.Role == "primary" {
			delete(st.Workers.Placements, key)
		}
	}
	timestamp := time.Now().UTC()
	placement := domain.WorkerPlacement{
		Service: ws.Service, Organization: ws.Organization, NetworkProfile: ws.NetworkProfile,
		Pool: pool, Role: "primary", State: "active", Revision: 1, Created: timestamp, Updated: timestamp,
	}
	st.Workers.Placements[domain.WorkerPlacementKey(ws.Service, pool)] = placement
	return placement
}

// workerServiceView is the read-only form of workerService: it applies the same
// defaults to the returned value without touching the stored row. Read endpoints
// must use this so a GET never mutates the service row.
//
// The environment map is copied first: normalizeService writes into it in
// place, so returning the stored map by reference would let a read mutate the
// loaded state (and, in the memory store, be observed as a write).
func (s *Service) WorkerServiceView(st *domain.State, t authorization.Target) domain.WorkerService {
	if st.Workers == nil {
		st.Workers = domain.NewWorkerState()
	}
	ws, ok := st.Workers.Services[t.ID]
	if !ok {
		ws = domain.WorkerService{Service: t.ID, Organization: t.Organization, Domains: []string{}, Environments: map[string]domain.WorkerEnvironment{}}
	}
	ws.Organization = t.Organization
	ws.Domains = append([]string(nil), ws.Domains...)
	environments := make(map[string]domain.WorkerEnvironment, len(ws.Environments))
	for name, env := range ws.Environments {
		environments[name] = env
	}
	ws.Environments = environments
	return s.NormalizeService(ws, st)
}

// normalizeService applies catalog defaults and derives hosts. Hosts are never
// persisted: routing, the edge projection and API responses all compute them
// here, so they cannot drift apart.
func (s *Service) NormalizeService(ws domain.WorkerService, states ...*domain.State) domain.WorkerService {
	var state *domain.WorkerState
	if len(states) > 0 {
		state = states[0].Workers
	}
	ws = s.Config.Workers.NormalizeServiceNetwork(ws)
	pool := s.Config.Workers.ServicePool(state, ws)
	if ws.Environments == nil {
		ws.Environments = map[string]domain.WorkerEnvironment{}
	}
	// The baseline always exists and always serves production traffic.
	baseline, ok := ws.Environments[appconfig.WorkerBaseline]
	if !ok {
		baseline = domain.WorkerEnvironment{ConfigRevision: 1}
	}
	baseline.Name = appconfig.WorkerBaseline
	baseline.Enabled = true
	if baseline.ConfigRevision == 0 {
		baseline.ConfigRevision = 1
	}
	if baseline.Variables == nil {
		baseline.Variables = map[string]string{}
	}
	if baseline.SecretVersions == nil {
		baseline.SecretVersions = map[string]string{}
	}
	baseline.SecretNames = domain.WorkerSecretNames(baseline.SecretVersions)
	ws.Environments[appconfig.WorkerBaseline] = baseline
	for name, env := range ws.Environments {
		if env.SecretVersions == nil {
			env.SecretVersions = map[string]string{}
		}
		env.SecretNames = domain.WorkerSecretNames(env.SecretVersions)
		s.SyncEnvironmentHosts(&env, ws.Organization, ws.Service, ws.Domains, ws.NetworkProfile, pool)
		ws.Environments[name] = env
	}
	return ws
}

type WorkerOverviewEnvironment struct {
	Environment      domain.WorkerEnvironment `json:"environment"`
	History          []domain.WorkerHistory   `json:"history"`
	CurrentVersion   *domain.WorkerVersion    `json:"current_version,omitempty"`
	ConfirmedVersion *domain.WorkerVersion    `json:"confirmed_version,omitempty"`
}

func (s *Service) WorkerOverview(ctx context.Context, st *domain.State, p authorization.Principal, ref ServiceRef) (any, Outcome, error) {
	t, err := s.WorkerCollectionTarget(st, ref, "service.read", p)
	if err != nil {
		return nil, Read, err
	}
	ws := s.WorkerServiceView(st, t)
	names := make([]string, 0, len(ws.Environments))
	for name := range ws.Environments {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		if names[i] == appconfig.WorkerBaseline {
			return true
		}
		if names[j] == appconfig.WorkerBaseline {
			return false
		}
		return names[i] < names[j]
	})

	environments := []WorkerOverviewEnvironment{}
	versionVisible := map[string]bool{}
	versionIDs := map[string]bool{}
	for _, name := range names {
		target := authorization.Target{Kind: "service", ID: t.ID, Organization: t.Organization, Environment: name}
		read, authErr := authorization.Authorize(st, p, "service.read", target)
		if authErr != nil {
			return nil, Read, authErr
		}
		if !read.Allowed {
			continue
		}
		env := ws.Environments[name]
		s.SyncEnvironmentHosts(&env, t.Organization, t.ID, ws.Domains, ws.NetworkProfile, s.Config.Workers.ServicePool(st.Workers, ws))
		entry := WorkerOverviewEnvironment{Environment: env, History: []domain.WorkerHistory{}}
		versions, authErr := authorization.Authorize(st, p, "service.version.read", target)
		if authErr != nil {
			return nil, Read, authErr
		}
		if versions.Allowed {
			versionVisible[name] = true
			entry.History = append(entry.History, st.Workers.Histories[t.ID+"/"+name]...)
			for _, history := range entry.History {
				versionIDs[history.Version] = true
			}
			if version, ok := st.Workers.Versions[env.Version]; ok {
				value := PublicWorkerVersion(version)
				entry.CurrentVersion = &value
				versionIDs[version.ID] = true
			}
			if version, ok := st.Workers.Versions[env.Confirmed]; ok {
				value := PublicWorkerVersion(version)
				entry.ConfirmedVersion = &value
				versionIDs[version.ID] = true
			}
		}
		environments = append(environments, entry)
	}

	builds := []domain.WorkerBuild{}
	for _, build := range st.Workers.Builds {
		if build.Service == t.ID && build.Organization == t.Organization && versionVisible[build.Environment] {
			builds = append(builds, build)
		}
	}
	sort.Slice(builds, func(i, j int) bool { return builds[i].Created.After(builds[j].Created) })

	operations := []domain.WorkerOperation{}
	for _, operation := range st.Workers.Operations {
		if operation.Service == t.ID && operation.Organization == t.Organization && versionVisible[operation.Environment] {
			operations = append(operations, operation)
		}
	}
	sort.Slice(operations, func(i, j int) bool { return operations[i].Created.After(operations[j].Created) })
	if len(operations) > 100 {
		operations = operations[:100]
	}
	versions := map[string]domain.WorkerVersion{}
	for id,version := range st.Workers.Versions {if version.Service==t.ID && version.Organization==t.Organization && versionVisible[version.Environment] {versionIDs[id]=true}}
	for id := range versionIDs {
		if version, ok := st.Workers.Versions[id]; ok && version.Service == t.ID && version.Organization == t.Organization && versionVisible[version.Environment] {
			versions[id] = PublicWorkerVersion(version)
		}
	}
	return map[string]any{
		"builds": builds, "environments": environments, "operations": operations, "versions": versions,
		"domains_revision": ws.DomainsRevision,
	}, Read, nil

}

func (s *Service) WorkerServiceDomains(ctx context.Context, st *domain.State, p authorization.Principal, ref ServiceRef) (any, Outcome, error) {
	t, e := s.WorkerTarget(st, ref, appconfig.WorkerBaseline, "service.read", p)
	if e != nil {
		return nil, Read, e
	}
	ws := s.WorkerServiceView(st, t)
	profile, _ := s.Config.Workers.NetworkProfile(ws.NetworkProfile)
	return map[string]any{
		"selected": s.SyncServiceDomains(ws), "catalog": s.DomainCatalogForProfile(profile), "revision": ws.DomainsRevision,
		"network_profile": WorkerNetworkProfileView{ID: profile.ID, Name: profile.Name, Description: profile.Description, ApprovalRequired: profile.Approval == "required", Default: profile.Default},
	}, Read, nil

}

func (s *Service) WorkerServiceDomainsUpdate(ctx context.Context, st *domain.State, p authorization.Principal, ref ServiceRef, t authorization.Target, in WorkerServiceDomainsUpdateInput) (any, Outcome, error) {
	if len(in.Domains) == 0 {
		return nil, Read, domain.Problem(422, "DOMAINS_INVALID", "Select at least one domain")
	}
	ws := s.WorkerService(st, t)
	profile, ok := s.Config.Workers.NetworkProfile(ws.NetworkProfile)
	if !ok {
		return nil, Read, domain.Problem(409, "WORKER_NETWORK_PROFILE_INVALID", "The service network profile is no longer available")
	}
	allowed := map[string]bool{}
	for _, domain := range s.Config.Workers.DomainsForProfile(profile) {
		allowed[domain.ID] = true
	}
	seen := map[string]bool{}
	for _, id := range in.Domains {
		if !allowed[id] {
			return nil, Read, domain.Problem(422, "DOMAINS_INVALID", "Domain is not allowed by the selected network profile: "+id)
		}
		if seen[id] {
			return nil, Read, domain.Problem(422, "DOMAINS_INVALID", "Duplicate domain: "+id)
		}
		seen[id] = true
	}
	if in.ExpectedRevision == nil {
		return nil, Read, domain.Problem(422, "DOMAINS_INVALID", "expected_revision is required")
	}
	if *in.ExpectedRevision != ws.DomainsRevision {
		return nil, Read, domain.Problem(409, "REVISION_CONFLICT", "Domain configuration changed")
	}
	ws.Domains = append([]string(nil), in.Domains...)
	ws.DomainsRevision++
	selected := s.SyncServiceHosts(&ws)
	st.Workers.Services[t.ID] = ws
	s.audit(st, p, "workers.domains.update", t.ID, true)
	return map[string]any{"selected": selected, "catalog": s.DomainCatalogForProfile(profile), "revision": ws.DomainsRevision}, Read, nil

}

func (s *Service) WorkerServiceNetworkProfileUpdate(ctx context.Context, st *domain.State, p authorization.Principal, ref ServiceRef, t authorization.Target, in WorkerServiceNetworkProfileUpdateInput) (any, Outcome, error) {
	if in.ExpectedRevision == nil {
		return nil, Read, domain.Problem(422, "NETWORK_PROFILE_INVALID", "expected_revision is required")
	}
	profile, ok := s.Config.Workers.NetworkProfile(in.Profile)
	if !ok || profile.Visibility != "public" {
		return nil, Read, domain.Problem(404, "NETWORK_PROFILE_NOT_FOUND", "Network profile not found")
	}
	if profile.Approval == "required" {
		return nil, Read, domain.Problem(403, "NETWORK_PROFILE_APPROVAL_REQUIRED", "This network profile requires platform approval")
	}
	pool, ok := s.Config.Workers.SelectPoolForService(profile.ID, t.ID)
	if !ok {
		return nil, Read, domain.Problem(409, "NETWORK_PROFILE_UNAVAILABLE", "The selected network profile has no available runtime placement")
	}
	ws := s.WorkerService(st, t)
	if ws.DomainsRevision != *in.ExpectedRevision {
		return nil, Read, domain.Problem(409, "REVISION_CONFLICT", "Network configuration changed")
	}
	if ws.NetworkProfile == profile.ID {
		return s.WorkerServiceDomains(ctx, st, p, ref)
	}
	for _, env := range ws.Environments {
		if env.Version != "" {
			return nil, Read, domain.Problem(409, "WORKER_NETWORK_REASSIGNMENT_REQUIRED", "Published Workers require a platform-managed runtime reassignment before changing network profile")
		}
	}
	ws.NetworkProfile = profile.ID
	ws.Domains = s.Config.Workers.DefaultDomainsForProfile(profile)
	ws.DomainsRevision++
	selected := s.SyncServiceHosts(&ws)
	st.Workers.Services[t.ID] = ws
	s.ReplaceWorkerPlacement(st, ws, pool.ID)
	s.audit(st, p, "workers.network-profile.update", t.ID, true)
	return map[string]any{
		"selected": selected, "catalog": s.DomainCatalogForProfile(profile), "revision": ws.DomainsRevision,
		"network_profile": WorkerNetworkProfileView{ID: profile.ID, Name: profile.Name, Description: profile.Description, ApprovalRequired: false, Default: profile.Default},
	}, Read, nil

}

// syncEnvironmentHosts recomputes an environment's Hosts from the service's
// domain selection. Every write path funnels through it so persisted hosts, the
// edge projection and the HTTPRoutes cannot drift apart. Domains that no longer
// exist in the catalog are dropped.
func (s *Service) SyncEnvironmentHosts(env *domain.WorkerEnvironment, organization, service string, serviceDomains []string, profiles ...string) {
	if s.Config.Workers == nil || env == nil {
		return
	}
	c := s.Config.Workers
	slug := appconfig.WorkerSlug(organization, service)
	selected := map[string]bool{}
	for _, id := range serviceDomains {
		selected[id] = true
	}
	// Emit in catalog order so Domains[i] and Hosts[i] stay aligned for clients.
	env.Domains = make([]string, 0, len(selected))
	env.Hosts = make([]string, 0, len(selected))
	env.DisplayHosts = map[string]string{}
	for _, d := range c.Domains {
		if !selected[d.ID] {
			continue
		}
		suffix := d.Domain
		{
			profile := c.DefaultNetworkProfile().ID
			if len(profiles) > 0 {
				profile = profiles[0]
			}
			if len(profiles) > 1 && profiles[1] != "" {
				suffix = c.PoolHostSuffix(profiles[1], d.Domain)
			} else if pool, ok := c.SelectPoolForService(profile, service); ok {
				suffix = c.PoolHostSuffix(pool.ID, d.Domain)
			} else {
				continue
			}
		}
		host := appconfig.HostFor(slug, env.Name, suffix)
		if host == "" {
			continue
		}
		env.Domains = append(env.Domains, d.ID)
		env.Hosts = append(env.Hosts, host)
		env.DisplayHosts[d.ID] = host
	}
}

// syncServiceHosts applies the service's domain selection to every environment
// and returns the normalised (catalog-ordered) selection.
func (s *Service) SyncServiceHosts(ws *domain.WorkerService) []string {
	if s.Config.Workers == nil || ws == nil {
		return nil
	}
	ws.Domains = s.SyncServiceDomains(*ws)
	for name, env := range ws.Environments {
		s.SyncEnvironmentHosts(&env, ws.Organization, ws.Service, ws.Domains, ws.NetworkProfile)
		ws.Environments[name] = env
	}
	return ws.Domains
}

// syncServiceDomains returns the selection limited to catalog entries, in
// catalog order, without touching any environment. Read endpoints use it so a
// GET never rewrites a service row.
func (s *Service) SyncServiceDomains(ws domain.WorkerService) []string {
	if s.Config.Workers == nil {
		return ws.Domains
	}
	selected := map[string]bool{}
	for _, id := range ws.Domains {
		selected[id] = true
	}
	kept := make([]string, 0, len(selected))
	for _, d := range s.Config.Workers.Domains {
		if selected[d.ID] {
			kept = append(kept, d.ID)
		}
	}
	if len(kept) == 0 {
		return s.Config.Workers.DefaultDomains()
	}
	return kept
}

type WorkerServiceSummary struct {
	domain.Resource
	Repository     *domain.WorkerRepositorySource `json:"repository_source,omitempty"`
	NetworkProfile WorkerNetworkProfileView       `json:"network_profile"`
	Environments   []WorkerEnvironmentSummary     `json:"environments"`
}

// WorkerDomainView is the client-facing domain catalog entry. It deliberately
// omits access classes, selectors, Gateway and pool identifiers: organization
// users only see domains made available by their selected network profile.
type WorkerDomainView struct {
	ID      string `json:"id"`
	Domain  string `json:"domain"`
	Label   string `json:"label"`
	Default bool   `json:"default"`
}

type WorkerNetworkProfileView struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Description      string `json:"description"`
	ApprovalRequired bool   `json:"approval_required"`
	Default          bool   `json:"default"`
}

func (s *Service) WorkerNetworkProfiles(ctx context.Context, st *domain.State, p authorization.Principal, ref ServiceRef) (any, Outcome, error) {
	target, err := s.orgTarget(st, ref)
	if err != nil {
		return nil, Read, err
	}
	if err := s.require(st, p, "service.list", target); err != nil {
		return nil, Read, err
	}
	out := []WorkerNetworkProfileView{}
	for _, profile := range s.Config.Workers.ConfiguredNetworkProfiles() {
		if profile.Visibility != "public" {
			continue
		}
		out = append(out, WorkerNetworkProfileView{ID: profile.ID, Name: profile.Name, Description: profile.Description, ApprovalRequired: profile.Approval == "required", Default: profile.Default})
	}
	return collection(out)

}

// domainCatalog renders the platform domain catalog for clients.
func (s *Service) DomainCatalog() []WorkerDomainView {
	out := make([]WorkerDomainView, 0, len(s.Config.Workers.Domains))
	for _, d := range s.Config.Workers.Domains {
		out = append(out, WorkerDomainView{ID: d.ID, Domain: d.Domain, Label: d.Label, Default: d.Default})
	}
	return out
}

func (s *Service) DomainCatalogForProfile(profile appconfig.WorkerNetworkProfile) []WorkerDomainView {
	allowed := map[string]bool{}
	for _, domain := range s.Config.Workers.DomainsForProfile(profile) {
		allowed[domain.ID] = true
	}
	out := []WorkerDomainView{}
	for _, domain := range s.DomainCatalog() {
		if allowed[domain.ID] {
			out = append(out, domain)
		}
	}
	return out
}

func (s *Service) WorkerDomains(ctx context.Context, st *domain.State, p authorization.Principal, ref ServiceRef) (any, Outcome, error) {
	target, err := s.orgTarget(st, ref)
	if err != nil {
		return nil, Read, err
	}
	if err := s.require(st, p, "service.list", target); err != nil {
		return nil, Read, err
	}
	seen := map[string]bool{}
	out := []WorkerDomainView{}
	for _, profile := range s.Config.Workers.ConfiguredNetworkProfiles() {
		if profile.Visibility != "public" {
			continue
		}
		for _, domain := range s.DomainCatalogForProfile(profile) {
			if !seen[domain.ID] {
				seen[domain.ID] = true
				out = append(out, domain)
			}
		}
	}
	return collection(out)

}

func (s *Service) WorkerServiceList(ctx context.Context, st *domain.State, p authorization.Principal, ref ServiceRef) (any, Outcome, error) {
	target, err := s.orgTarget(st, ref)
	if err != nil {
		return nil, Read, err
	}
	if err := s.require(st, p, "service.list", target); err != nil {
		return nil, Read, err
	}
	out := make([]WorkerServiceSummary, 0)
	for _, resource := range st.Resources {
		if resource.Kind != "service" || resource.Organization != target.ID {
			continue
		}
		summary := WorkerServiceSummary{Resource: resource, Environments: []WorkerEnvironmentSummary{}}
		service := s.Config.Workers.NormalizeServiceNetwork(st.Workers.Services[resource.ID])
		if profile, ok := s.Config.Workers.NetworkProfile(service.NetworkProfile); ok {
			summary.NetworkProfile = WorkerNetworkProfileView{ID: profile.ID, Name: profile.Name, Description: profile.Description, ApprovalRequired: profile.Approval == "required", Default: profile.Default}
		}
		names := make([]string, 0, len(service.Environments)+1)
		if _, exists := service.Environments[appconfig.WorkerBaseline]; !exists {
			names = append(names, appconfig.WorkerBaseline)
		}
		for name := range service.Environments {
			names = append(names, name)
		}
		sort.Slice(names, func(i, j int) bool {
			if names[i] == appconfig.WorkerBaseline {
				return true
			}
			if names[j] == appconfig.WorkerBaseline {
				return false
			}
			return names[i] < names[j]
		})
		visible := false
		for _, env := range names {
			decision, err := authorization.Authorize(st, p, "service.read", authorization.Target{Kind: "service", ID: resource.ID, Organization: target.ID, Environment: env})
			if err != nil {
				return nil, Read, err
			}
			if !decision.Allowed {
				continue
			}
			visible = true
			if current, exists := service.Environments[env]; exists {
				// Hosts are derived for display; the persisted row is not rewritten
				// from a read path.
				s.SyncEnvironmentHosts(&current, resource.Organization, resource.ID, s.SyncServiceDomains(service), service.NetworkProfile, s.Config.Workers.ServicePool(st.Workers, service))
				summary.Environments = append(summary.Environments, WorkerEnvironmentSummary{Name: env, Domains: current.Domains, Hosts: current.Hosts, Enabled: current.Enabled, Version: current.Version, Confirmed: current.Confirmed})
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
	return out, Read, nil

}

func (s *Service) WorkerRepositoryProviders(ctx context.Context, st *domain.State, p authorization.Principal, ref ServiceRef) (any, Outcome, error) {
	target, err := s.orgTarget(st, ref)
	if err != nil {
		return nil, Read, err
	}
	if err := s.require(st, p, "service.list", target); err != nil {
		return nil, Read, err
	}
	connected, err := s.Store.ConnectedRepositoryProviders(ctx, p.User.ID)
	if err != nil {
		return nil, Read, err
	}
	return collection(s.Repository.VisibleRepositoryProviders(target.ID, connected))

}

func (s *Service) WorkerRepositoryGet(ctx context.Context, st *domain.State, p authorization.Principal, ref ServiceRef) (any, Outcome, error) {
	target, err := s.WorkerCollectionTarget(st, ref, "service.read", p)
	if err != nil {
		return nil, Read, err
	}
	service := s.WorkerServiceView(st, target)
	return map[string]any{"source": service.Repository, "revision": service.RepositoryRevision}, Read, nil

}

func (s *Service) WorkerRepositoryUpdate(ctx context.Context, st *domain.State, p authorization.Principal, ref ServiceRef, target authorization.Target, in WorkerRepositoryUpdateInput) (any, Outcome, error) {
	service := s.WorkerService(st, target)
	if service.RepositoryRevision != in.Revision {
		return nil, Read, domain.Problem(409, "REVISION_CONFLICT", "Repository configuration changed")
	}
	if in.Source == nil {
		service.Repository = nil
	} else {
		if _, err := s.Repository.ValidateRepositorySource(in.Source, target.Organization); err != nil {
			return nil, Read, err
		}
		service.Repository = in.Source
	}
	service.RepositoryRevision++
	st.Workers.Services[target.ID] = service
	return map[string]any{"source": service.Repository, "revision": service.RepositoryRevision}, Read, nil

}
