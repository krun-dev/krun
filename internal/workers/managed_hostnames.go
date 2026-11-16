package workers

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/krun-dev/krun/internal/authorization"
	appconfig "github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/domain"
)

var managedLabel = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)
var managedPath = regexp.MustCompile(`^/(?:[A-Za-z0-9._~-]+(?:/[A-Za-z0-9._~-]+)*/?)?$`)

type ManagedHostnameInput struct {
	SuffixID         string                        `json:"suffix_id"`
	Label            string                        `json:"label"`
	ExpectedRevision int                           `json:"expected_revision"`
	Routes           []domain.ManagedHostnameRoute `json:"routes"`
}

type WorkerCustomDomainInput struct {
	SuffixID string `json:"suffix_id"`
	Label    string `json:"label"`
}

func (s *Service) typedHostnames(items []domain.ManagedHostname, types ...appconfig.WorkerDomainType) []domain.ManagedHostname {
	out := []domain.ManagedHostname{}
	for _, item := range items {
		suffix, ok := s.Config.Workers.Domain(item.SuffixID)
		kind := appconfig.WorkerDomainType(item.Type)
		if ok && suffix.Allows(kind) && slices.Contains(types, kind) {
			out = append(out, item)
		}
	}
	return out
}

func (s *Service) ManagedHostnameCatalog(ctx context.Context, st *domain.State, p authorization.Principal, ref WorkerRef) (any, Outcome, error) {
	target, err := s.orgTarget(st, ref)
	if err != nil {
		return nil, Read, err
	}
	if err = s.require(st, p, "worker.hostname.read", target); err != nil {
		return nil, Read, err
	}
	items, err := s.Store.ManagedHostnames(ctx, target.ID)
	if err != nil {
		return nil, Read, err
	}
	suffixes := []WorkerDomainView{}
	for _, item := range s.DomainCatalog() {
		if slices.Contains(item.Types, appconfig.WorkerDomainApplication) {
			suffixes = append(suffixes, item)
		}
	}
	decision, err := authorization.Authorize(st, p, "worker.hostname.manage", target)
	if err != nil {
		return nil, Read, err
	}
	type originOption struct {
		Host     string   `json:"host"`
		Kind     string   `json:"kind"`
		Label    string   `json:"label"`
		Suffixes []string `json:"suffixes"`
	}
	origins := []originOption{}
	for _, origin := range s.Config.Workers.ManagedOrigins {
		if slices.Contains(origin.Organizations, target.ID) {
			origins = append(origins, originOption{origin.Host, origin.Kind, origin.Label, origin.Suffixes})
		}
	}
	return map[string]any{"items": s.typedHostnames(items, appconfig.WorkerDomainApplication), "suffixes": suffixes, "origins": origins, "can_manage": decision.Allowed}, Read, nil
}

func (s *Service) WorkerManagedHostnames(ctx context.Context, st *domain.State, p authorization.Principal, ref WorkerRef) (any, Outcome, error) {
	target, err := s.WorkerTarget(st, ref, appconfig.WorkerBaseline, "worker.read", p)
	if err != nil {
		return nil, Read, err
	}
	org, err := s.orgTarget(st, ref)
	if err != nil {
		return nil, Read, err
	}
	decision, err := authorization.Authorize(st, p, "worker.hostname.read", org)
	if err != nil {
		return nil, Read, err
	}
	manage, err := authorization.Authorize(st, p, "worker.hostname.manage", org)
	if err != nil {
		return nil, Read, err
	}
	update, err := authorization.Authorize(st, p, "worker.update", target)
	if err != nil {
		return nil, Read, err
	}
	customSuffixes := []WorkerDomainView{}
	canManage := manage.Allowed && update.Allowed
	applicationHosts := []domain.ManagedHostnameOption{}
	profile := s.WorkerView(st, target).NetworkProfile
	if canManage && decision.Allowed {
		suffixIDs := s.applicationSuffixesForWorker(st, target.ID, profile)
		if len(suffixIDs) > 0 {
			applicationHosts, err = s.Store.ApplicationHostnameOptions(ctx, org.ID, suffixIDs)
			if err != nil {
				return nil, Read, err
			}
		}
	}
	// Read bindings after the options so a stale empty selection cannot carry
	// a revision newer than an intervening route added to this Worker.
	bindings, err := s.Store.WorkerManagedHostnames(ctx, target.Organization, target.ID)
	if err != nil {
		return nil, Read, err
	}
	for _, suffix := range s.DomainCatalog() {
		if slices.Contains(suffix.Types, appconfig.WorkerDomainCustom) && s.Config.Workers.ManagedDomainAllowsWorker(suffix.ID, profile) {
			customSuffixes = append(customSuffixes, suffix)
		}
	}
	return map[string]any{
		"items":    s.typedHostnames(bindings, appconfig.WorkerDomainCustom, appconfig.WorkerDomainApplication),
		"suffixes": customSuffixes, "application_hosts": applicationHosts,
		"can_manage": canManage, "can_read_catalog": decision.Allowed,
	}, Read, nil
}

func (s *Service) applicationSuffixesForWorker(st *domain.State, worker, profile string) []string {
	placement, placed := domain.ActiveWorkerPlacement(st.Workers, worker)
	if !placed || placement.NetworkProfile != profile {
		return nil
	}
	var ids []string
	for _, suffix := range s.Config.Workers.Domains {
		if !suffix.Allows(appconfig.WorkerDomainApplication) || !s.Config.Workers.ManagedDomainAllowsWorker(suffix.ID, profile) {
			continue
		}
		if pool, err := s.Config.Workers.DomainRuntimePool(suffix.ID); err == nil && pool.ID == placement.Pool {
			ids = append(ids, suffix.ID)
		}
	}
	return ids
}

func (s *Service) SaveManagedHostname(ctx context.Context, st *domain.State, p authorization.Principal, ref WorkerRef, host string, in ManagedHostnameInput, retire bool) (any, Outcome, error) {
	org, err := s.orgTarget(st, ref)
	if err != nil {
		return nil, Read, err
	}
	if err = s.require(st, p, "worker.hostname.manage", org); err != nil {
		return nil, Read, err
	}
	item, err := s.managedHostnameForChange(ctx, org.ID, host, in, appconfig.WorkerDomainApplication)
	if err != nil {
		return nil, Read, err
	}
	if retire {
		item.Retired = true
		item.Routes = nil
	} else {
		if host != "" && in.Routes == nil {
			return nil, Read, domain.Invalid("HOSTNAME_ROUTE_INVALID", "Provide routes explicitly; use [] to remove all routes")
		}
		if err = s.replaceApplicationRoutes(ctx, st, p, &item, in.Routes); err != nil {
			return nil, Read, err
		}
	}
	action := "workers.hostnames.update"
	if retire {
		action = "workers.hostnames.retire"
	} else if host == "" {
		action = "workers.hostnames.claim"
	}
	item, err = s.saveHostname(ctx, st, p, item, in.ExpectedRevision, action)
	if err != nil {
		return nil, Read, err
	}
	if retire {
		return nil, Deleted, nil
	}
	if host == "" {
		return item, Created, nil
	}
	return item, Read, nil
}

func (s *Service) SaveWorkerApplicationRoutes(ctx context.Context, st *domain.State, p authorization.Principal, ref WorkerRef, host string, in ManagedHostnameInput) (any, Outcome, error) {
	org, err := s.orgTarget(st, ref)
	if err != nil {
		return nil, Read, err
	}
	if err = s.require(st, p, "worker.hostname.manage", org); err != nil {
		return nil, Read, err
	}
	target, err := s.WorkerTarget(st, ref, appconfig.WorkerBaseline, "worker.update", p)
	if err != nil {
		return nil, Read, err
	}
	item, err := s.managedHostnameForChange(ctx, org.ID, host, in, appconfig.WorkerDomainApplication)
	if err != nil {
		return nil, Read, err
	}
	if in.Routes == nil {
		return nil, Read, domain.Invalid("HOSTNAME_ROUTE_INVALID", "Provide routes explicitly; use [] to remove all routes")
	}
	routes := make([]domain.ManagedHostnameRoute, 0, len(item.Routes)+len(in.Routes))
	for _, rule := range item.Routes {
		if rule.Worker != target.ID {
			routes = append(routes, rule)
		}
	}
	for _, rule := range in.Routes {
		if rule.Origin != "" || (rule.Worker != "" && rule.Worker != target.ID && rule.Worker != st.Resources[target.ID].Locator) {
			return nil, Read, domain.Invalid("HOSTNAME_TARGET_INVALID", "The target must be the current Worker")
		}
		rule.Worker = target.ID
		routes = append(routes, rule)
	}
	if err = s.replaceApplicationRoutes(ctx, st, p, &item, routes); err != nil {
		return nil, Read, err
	}
	item, err = s.saveHostname(ctx, st, p, item, in.ExpectedRevision, "workers.hostnames.update")
	if err != nil {
		return nil, Read, err
	}
	item.Routes = slices.DeleteFunc(item.Routes, func(rule domain.ManagedHostnameRoute) bool {
		return rule.Worker != target.ID
	})
	return item, Read, nil
}

func (s *Service) replaceApplicationRoutes(ctx context.Context, st *domain.State, p authorization.Principal, item *domain.ManagedHostname, routes []domain.ManagedHostnameRoute) error {
	normalized, err := s.ValidateManagedHostnameRoutes(ctx, st, p, item.Organization, item.SuffixID, routes, item.Routes)
	if err != nil {
		return err
	}
	for _, old := range item.Routes {
		resource, exists := st.Resources[old.Worker]
		if !exists || resource.Kind != "worker" || resource.Organization != item.Organization {
			continue
		}
		// Updating a route target or removing a route changes the old
		// Worker's traffic as well. Leaving it in place needs no permission
		// on that Worker, and deleted targets can still be cleaned up.
		if !slices.ContainsFunc(normalized, func(rule domain.ManagedHostnameRoute) bool {
			return rule.Match == old.Match && rule.Path == old.Path && rule.Worker == old.Worker
		}) {
			ref := WorkerRef{Organization: item.Organization, Worker: old.Worker}
			if _, err := s.WorkerTarget(st, ref, appconfig.WorkerBaseline, "worker.update", p); err != nil {
				return err
			}
		}
	}
	item.Routes = normalized
	return PrepareGatewayHostname(item)
}

func (s *Service) saveHostname(ctx context.Context, st *domain.State, p authorization.Principal, item domain.ManagedHostname, revision int, action string) (domain.ManagedHostname, error) {
	before := len(st.PendingAudits)
	defer func() { st.PendingAudits = st.PendingAudits[:before] }()
	s.audit(st, p, action, item.Host, true)
	for i := before; i < len(st.PendingAudits); i++ {
		st.PendingAudits[i].Organization = item.Organization
	}
	return s.Store.SaveManagedHostname(ctx, item, revision, st.PendingAudits[before:])
}

func (s *Service) SaveWorkerCustomDomain(ctx context.Context, st *domain.State, p authorization.Principal, ref WorkerRef, host string, in WorkerCustomDomainInput, revision int) (any, Outcome, error) {
	org, err := s.orgTarget(st, ref)
	if err != nil {
		return nil, Read, err
	}
	if err = s.require(st, p, "worker.hostname.manage", org); err != nil {
		return nil, Read, err
	}
	target, err := s.WorkerTarget(st, ref, appconfig.WorkerBaseline, "worker.update", p)
	if err != nil {
		return nil, Read, err
	}
	item, err := s.managedHostnameForChange(ctx, org.ID, host, ManagedHostnameInput{
		SuffixID: in.SuffixID, Label: in.Label, ExpectedRevision: revision,
	}, appconfig.WorkerDomainCustom)
	if err != nil {
		return nil, Read, err
	}

	if host != "" {
		if len(item.Routes) != 1 || !item.Routes[0].CustomDomain || item.Routes[0].Worker != target.ID {
			return nil, Read, domain.NotFound()
		}
		item.Routes = []domain.ManagedHostnameRoute{}
		_, err = s.saveHostname(ctx, st, p, item, revision, "workers.custom-domains.detach")
		return nil, Deleted, err
	}

	existing, err := s.Store.ManagedHostname(ctx, org.ID, item.Host)
	if err == nil {
		if existing.SuffixID != item.SuffixID || existing.Type != item.Type || len(existing.Routes) != 0 {
			return nil, Read, domain.Conflict("HOSTNAME_UNAVAILABLE", "Hostname is unavailable")
		}
		item.Revision = existing.Revision
	} else {
		var problem *domain.APIError
		if !errors.As(err, &problem) || problem.Kind != domain.ErrorNotFound {
			return nil, Read, err
		}
	}
	rules, err := s.ValidateManagedHostnameRoutes(ctx, st, p, org.ID, item.SuffixID, []domain.ManagedHostnameRoute{
		{Match: "prefix", Path: "/", Worker: target.ID, Enabled: true},
	}, nil)
	if err != nil {
		return nil, Read, err
	}
	rules[0].CustomDomain = true
	rules[0].PreviewsEnabled = true
	item.Routes = rules
	out, err := s.saveHostname(ctx, st, p, item, item.Revision, "workers.custom-domains.attach")
	return out, Created, err
}

func (s *Service) managedHostnameForChange(ctx context.Context, org, host string, in ManagedHostnameInput, kind appconfig.WorkerDomainType) (domain.ManagedHostname, error) {
	if host != "" {
		if in.ExpectedRevision < 1 {
			return domain.ManagedHostname{}, domain.Invalid("HOSTNAME_INVALID", "A positive expected_revision is required")
		}
		item, err := s.Store.ManagedHostname(ctx, org, strings.ToLower(host))
		if err != nil {
			return item, err
		}
		if item.Revision != in.ExpectedRevision {
			return item, domain.Conflict("REVISION_CONFLICT", "Hostname configuration changed")
		}
		suffix, ok := s.Config.Workers.Domain(item.SuffixID)
		if !ok || !suffix.Allows(kind) || item.Type != string(kind) {
			return domain.ManagedHostname{}, domain.NotFound()
		}
		return item, nil
	}

	if in.ExpectedRevision != 0 {
		return domain.ManagedHostname{}, domain.Invalid("HOSTNAME_INVALID", "A new hostname requires expected_revision 0")
	}
	return s.NewManagedHostname(org, in.SuffixID, in.Label, kind)
}

func (s *Service) NewManagedHostname(org, suffixID, prefix string, kind appconfig.WorkerDomainType) (domain.ManagedHostname, error) {
	suffix, ok := s.Config.Workers.Domain(suffixID)
	if !ok || !suffix.Allows(kind) || (kind != appconfig.WorkerDomainCustom && kind != appconfig.WorkerDomainApplication) {
		return domain.ManagedHostname{}, domain.Invalid("HOSTNAME_INVALID", "Choose a suffix of type "+string(kind))
	}
	label := strings.ToLower(prefix)
	host := label + "." + suffix.Domain
	if !managedLabel.MatchString(prefix) || len(host) > 253 {
		return domain.ManagedHostname{}, domain.Invalid("HOSTNAME_INVALID", "Use a single DNS label of 1–63 ASCII letters, digits or hyphens")
	}
	console, _ := url.Parse(s.Config.PublicURL)
	if s.Config.Workers.ManagedHostReserved(host) || (console != nil && strings.EqualFold(host, console.Hostname())) {
		return domain.ManagedHostname{}, domain.Conflict("HOSTNAME_UNAVAILABLE", "Hostname is unavailable")
	}
	return domain.ManagedHostname{Host: host, Organization: org, SuffixID: suffix.ID, Type: string(kind)}, nil
}

func (s *Service) ValidateManagedHostnameRoutes(ctx context.Context, st *domain.State, p authorization.Principal, org, suffixID string, in, previous []domain.ManagedHostnameRoute) ([]domain.ManagedHostnameRoute, error) {
	if len(in) > 64 {
		return nil, domain.Invalid("HOSTNAME_ROUTE_INVALID", "A hostname supports at most 64 routes")
	}
	suffixPool, poolErr := s.Config.Workers.DomainRuntimePool(suffixID)
	out := make([]domain.ManagedHostnameRoute, 0, len(in))
	seen := map[string]bool{}
	for _, rule := range in {
		if (rule.Match != "prefix" && rule.Match != "exact") || len(rule.Path) > 1024 || !managedPath.MatchString(rule.Path) {
			return nil, domain.Invalid("HOSTNAME_ROUTE_INVALID", "Use an exact or prefix match and an absolute path with unreserved characters")
		}
		for _, segment := range strings.Split(rule.Path, "/") {
			if segment == "." || segment == ".." {
				return nil, domain.Invalid("HOSTNAME_ROUTE_INVALID", "Dot segments are not allowed")
			}
		}
		if rule.Match == "prefix" && rule.Path != "/" {
			rule.Path = strings.TrimSuffix(rule.Path, "/")
		}
		key := rule.Match + ":" + rule.Path
		if seen[key] {
			return nil, domain.Conflict("HOSTNAME_ROUTE_CONFLICT", "A route already uses this match and path")
		}
		seen[key] = true
		if rule.Origin != "" {
			_, available := s.Config.Workers.ManagedOriginFor(org, suffixID, rule.Origin)
			unchanged := slices.ContainsFunc(previous, func(old domain.ManagedHostnameRoute) bool {
				return old.Match == rule.Match && old.Path == rule.Path && old.Origin == rule.Origin
			})
			if (!available && !unchanged) || rule.Worker != "" {
				return nil, domain.Invalid("HOSTNAME_TARGET_INVALID", "Choose an origin available to this organization and suffix")
			}
			rule.WorkerLocator = ""
			rule.CFPattern = ""
			rule.CustomDomain = false
			rule.PreviewsEnabled = false
			out = append(out, rule)
			continue
		}
		// Protocol metadata is server-owned. Preserve it only for a rule
		// that is still bound to the same Worker.
		rule.WorkerLocator = ""
		rule.CFPattern = ""
		rule.CustomDomain = false
		rule.PreviewsEnabled = false
		for _, old := range previous {
			if old.Match == rule.Match && old.Path == rule.Path && old.Worker == rule.Worker {
				rule.WorkerLocator = old.WorkerLocator
				rule.CFPattern = old.CFPattern
				rule.CustomDomain = old.CustomDomain
				break
			}
		}
		if !slices.ContainsFunc(previous, func(old domain.ManagedHostnameRoute) bool {
			return old.Match == rule.Match && old.Path == rule.Path && old.Worker == rule.Worker && old.Enabled == rule.Enabled
		}) {
			target, err := s.resolveManagedTarget(ctx, st, p, org, rule.Worker)
			if err != nil {
				return nil, err
			}
			rule.Worker = target.ID
			rule.WorkerLocator = st.Resources[target.ID].Locator
			worker := st.Workers.Workers[target.ID]
			placement, placed := domain.ActiveWorkerPlacement(st.Workers, target.ID)
			if poolErr != nil || !placed || placement.Pool != suffixPool.ID ||
				placement.NetworkProfile != worker.NetworkProfile || !s.Config.Workers.ManagedDomainAllowsWorker(suffixID, worker.NetworkProfile) {
				return nil, domain.Invalid("HOSTNAME_TARGET_INVALID", "The Worker network cannot use this suffix")
			}
		}
		out = append(out, rule)
	}
	return out, nil
}

func (s *Service) resolveManagedTarget(ctx context.Context, st *domain.State, p authorization.Principal, org, worker string) (authorization.Target, error) {
	id, err := s.Store.ResourceID(ctx, org, "worker", worker, true)
	var apiErr *domain.APIError
	if errors.As(err, &apiErr) && apiErr.Kind == domain.ErrorNotFound {
		return authorization.Target{}, domain.Invalid("HOSTNAME_TARGET_INVALID", "Choose a Worker in this organization")
	}
	if err != nil {
		return authorization.Target{}, err
	}
	return s.WorkerTarget(st, WorkerRef{Organization: org, Worker: id}, appconfig.WorkerBaseline, "worker.update", p)
}

// Gateway API allows at most sixteen rules, including the no-match response.
func PrepareGatewayHostname(item *domain.ManagedHostname) error {
	root := false
	for _, rule := range item.Routes {
		item.GatewayManaged = item.GatewayManaged || rule.Origin != ""
		root = root || (rule.Match == "prefix" && rule.Path == "/")
	}
	limit := 15
	if root {
		limit = 16
	}
	if item.GatewayManaged && len(item.Routes) > limit {
		return domain.Invalid("HOSTNAME_ROUTE_INVALID", "A gateway hostname exceeds the HTTPRoute rule limit")
	}
	return nil
}
