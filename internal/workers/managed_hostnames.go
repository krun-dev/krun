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
		suffix, _ := s.Config.Workers.Domain(item.ID)
		if suffix.ManagedSubdomains {
			suffixes = append(suffixes, item)
		}
	}
	decision, err := authorization.Authorize(st, p, "worker.hostname.manage", target)
	if err != nil {
		return nil, Read, err
	}
	return map[string]any{"items": items, "suffixes": suffixes, "can_manage": decision.Allowed}, Read, nil
}

func (s *Service) WorkerManagedHostnames(ctx context.Context, st *domain.State, p authorization.Principal, ref WorkerRef) (any, Outcome, error) {
	target, err := s.WorkerTarget(st, ref, appconfig.WorkerBaseline, "worker.read", p)
	if err != nil {
		return nil, Read, err
	}
	bindings, err := s.Store.WorkerManagedHostnames(ctx, target.Organization, target.ID)
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
	return map[string]any{"items": bindings, "can_read_catalog": decision.Allowed}, Read, nil
}

func (s *Service) SaveManagedHostname(ctx context.Context, st *domain.State, p authorization.Principal, ref WorkerRef, host string, in ManagedHostnameInput, retire bool) (any, Outcome, error) {
	org, err := s.orgTarget(st, ref)
	if err != nil {
		return nil, Read, err
	}
	if err = s.require(st, p, "worker.hostname.manage", org); err != nil {
		return nil, Read, err
	}
	item, err := s.managedHostnameForChange(ctx, org.ID, host, in)
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
		previous := item.Routes
		item.Routes, err = s.managedHostnameRoutes(ctx, st, p, org.ID, item.SuffixID, in.Routes, previous)
		if err != nil {
			return nil, Read, err
		}
	}
	action := "workers.hostnames.update"
	if retire {
		action = "workers.hostnames.retire"
	} else if host == "" {
		action = "workers.hostnames.claim"
	}
	before := len(st.PendingAudits)
	s.audit(st, p, action, item.Host, true)
	for i := before; i < len(st.PendingAudits); i++ {
		st.PendingAudits[i].Organization = org.ID
	}
	item, err = s.Store.SaveManagedHostname(ctx, item, in.ExpectedRevision, st.PendingAudits)
	if err != nil {
		return nil, Read, err
	}
	st.PendingAudits = nil
	if retire {
		return nil, Deleted, nil
	}
	if host == "" {
		return item, Created, nil
	}
	return item, Read, nil
}

func (s *Service) managedHostnameForChange(ctx context.Context, org, host string, in ManagedHostnameInput) (domain.ManagedHostname, error) {
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
		return item, nil
	}

	suffix, ok := s.Config.Workers.Domain(in.SuffixID)
	if !ok || !suffix.ManagedSubdomains {
		return domain.ManagedHostname{}, domain.Invalid("HOSTNAME_INVALID", "Choose a suffix that supports managed subdomains")
	}
	label := strings.ToLower(in.Label)
	host = label + "." + suffix.Domain
	if !managedLabel.MatchString(in.Label) || len(host) > 253 || in.ExpectedRevision != 0 {
		return domain.ManagedHostname{}, domain.Invalid("HOSTNAME_INVALID", "Use a single DNS label of 1–63 ASCII letters, digits or hyphens")
	}
	console, _ := url.Parse(s.Config.PublicURL)
	if s.Config.Workers.ManagedHostReserved(host) || (console != nil && strings.EqualFold(host, console.Hostname())) {
		return domain.ManagedHostname{}, domain.Conflict("HOSTNAME_UNAVAILABLE", "Hostname is unavailable")
	}
	return domain.ManagedHostname{Host: host, Organization: org, SuffixID: suffix.ID}, nil
}

func (s *Service) managedHostnameRoutes(ctx context.Context, st *domain.State, p authorization.Principal, org, suffixID string, in, previous []domain.ManagedHostnameRoute) ([]domain.ManagedHostnameRoute, error) {
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
