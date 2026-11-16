package cfcompat

import (
	"errors"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/krun-dev/krun/internal/authorization"
	appconfig "github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/securetoken"
	workerapp "github.com/krun-dev/krun/internal/workers"
)

type cfRouteInput struct {
	Pattern      string `json:"pattern"`
	Script       string `json:"script,omitempty"`
	ZoneID       string `json:"zone_id,omitempty"`
	ZoneName     string `json:"zone_name,omitempty"`
	CustomDomain bool   `json:"custom_domain,omitempty"`
}

type cfWorkerRoute struct {
	ID      string `json:"id"`
	Pattern string `json:"pattern"`
	Script  string `json:"script"`
}

func routePattern(host string, rule domain.ManagedHostnameRoute) string {
	if rule.CFPattern != "" {
		return rule.CFPattern
	}
	if rule.Match == "exact" {
		return host + rule.Path
	}
	return strings.TrimSuffix(host+rule.Path, "/") + "/*"
}

func workerRoute(host string, rule domain.ManagedHostnameRoute) cfWorkerRoute {
	return cfWorkerRoute{
		ID:      securetoken.Digest(host + "\x00" + rule.Match + "\x00" + rule.Path)[:32],
		Pattern: routePattern(host, rule), Script: rule.WorkerLocator,
	}
}

type hostnameChange struct {
	item     domain.ManagedHostname
	expected int
}

type hostnameChanges struct {
	app       *workerapp.Service
	state     *domain.State
	principal authorization.Principal
	request   *http.Request
	items     map[string]*hostnameChange
}

func (s *Server) authorizeHostnameChanges(st *domain.State, p authorization.Principal, r *http.Request) (*hostnameChanges, error) {
	target, err := s.OrgTarget(st, r)
	if err != nil {
		return nil, err
	}
	if err = s.Require(st, p, r, "worker.hostname.manage", target); err != nil {
		return nil, err
	}
	return &hostnameChanges{s.Application(r), st, p, r, map[string]*hostnameChange{}}, nil
}

func (c *hostnameChanges) edit(item domain.ManagedHostname) *hostnameChange {
	if change, ok := c.items[item.Host]; ok {
		return change
	}
	change := &hostnameChange{item: item, expected: item.Revision}
	c.items[item.Host] = change
	return change
}

func (c *hostnameChanges) hostname(host string, zone hostnameZone, kind appconfig.WorkerDomainType) (*hostnameChange, error) {
	if zone.Suffix.Type != kind {
		return nil, domain.Invalid("HOSTNAME_INVALID", "Choose a suffix of type "+string(kind))
	}
	host = strings.ToLower(host)
	label, ok := strings.CutSuffix(host, "."+zone.Name)
	if !ok || label == "" || strings.Contains(label, ".") {
		return nil, domain.Invalid("HOSTNAME_INVALID", "Choose a single subdomain label under the selected zone")
	}
	if change, ok := c.items[host]; ok {
		return change, nil
	}
	org := c.request.PathValue("org")
	item, err := c.app.Store.ManagedHostname(c.request.Context(), org, host)
	var problem *domain.APIError
	if errors.As(err, &problem) && problem.Kind == domain.ErrorNotFound {
		item, err = c.app.NewManagedHostname(org, zone.Suffix.ID, label, kind)
	}
	if err != nil {
		return nil, err
	}
	if item.SuffixID != zone.Suffix.ID {
		return nil, domain.Conflict("HOSTNAME_UNAVAILABLE", "Hostname is unavailable")
	}
	return c.edit(item), nil
}

func (c *hostnameChanges) save() error {
	auditStart := len(c.state.PendingAudits)
	defer func() { c.state.PendingAudits = c.state.PendingAudits[:auditStart] }()
	items := make([]domain.ManagedHostname, 0, len(c.items))
	revisions := make([]int, 0, len(c.items))
	for _, host := range slices.Sorted(maps.Keys(c.items)) {
		change := c.items[host]
		if len(change.item.Routes) > 64 {
			return domain.Invalid("HOSTNAME_ROUTE_INVALID", "A hostname supports at most 64 routes")
		}
		if err := workerapp.PrepareGatewayHostname(&change.item); err != nil {
			return err
		}
		items = append(items, change.item)
		revisions = append(revisions, change.expected)
		before := len(c.state.PendingAudits)
		c.app.Audit(c.state, c.principal, "workers.hostnames.update", host, true)
		for i := before; i < len(c.state.PendingAudits); i++ {
			c.state.PendingAudits[i].Organization = change.item.Organization
		}
	}
	if len(items) == 0 {
		return nil
	}
	return c.app.Store.SaveManagedHostnames(c.request.Context(), items, revisions, c.state.PendingAudits[auditStart:])
}

func (s *Server) routeZone(org, host, id, name string) (hostnameZone, error) {
	suffix, ok := s.Config.Workers.DomainForHost(strings.ToLower(host))
	if !ok || (id != "" && id != zoneID(org, suffix.ID)) || (name != "" && !strings.EqualFold(name, suffix.Domain)) {
		return hostnameZone{}, domain.Invalid("HOSTNAME_INVALID", "Choose a subdomain in a platform zone")
	}
	return hostnameZone{ID: zoneID(org, suffix.ID), Name: suffix.Domain, Suffix: suffix}, nil
}

func (c *hostnameChanges) addWorkerRoute(in cfRouteInput, zone hostnameZone) (cfWorkerRoute, error) {
	host, path, _ := strings.Cut(in.Pattern, "/")
	if in.CustomDomain || in.Script == "" || strings.ContainsAny(host, ":*?#") {
		return cfWorkerRoute{}, domain.Invalid("HOSTNAME_ROUTE_INVALID", "Use an exact hostname, a path and a Worker script")
	}
	match := "exact"
	path = "/" + path
	if strings.HasSuffix(path, "/*") {
		match = "prefix"
		path = strings.TrimSuffix(path, "*")
	}
	if strings.ContainsAny(path, "*?#") {
		return cfWorkerRoute{}, domain.Invalid("HOSTNAME_ROUTE_INVALID", "Use an exact path or a trailing /* path prefix")
	}
	change, err := c.hostname(host, zone, appconfig.WorkerDomainApplication)
	if err != nil {
		return cfWorkerRoute{}, err
	}
	rules, err := c.app.ValidateManagedHostnameRoutes(c.request.Context(), c.state, c.principal, change.item.Organization,
		change.item.SuffixID, []domain.ManagedHostnameRoute{{Match: match, Path: path, Worker: in.Script, Enabled: true}}, nil)
	if err != nil {
		return cfWorkerRoute{}, err
	}
	rule := rules[0]
	rule.CFPattern = strings.ToLower(host) + in.Pattern[len(host):]
	for _, existing := range change.item.Routes {
		if existing.Match == rule.Match && existing.Path == rule.Path {
			if existing.Worker == rule.Worker && !existing.CustomDomain && routePattern(change.item.Host, existing) == rule.CFPattern {
				return workerRoute(change.item.Host, existing), nil
			}
			return cfWorkerRoute{}, domain.Conflict("HOSTNAME_ROUTE_CONFLICT", "A route already uses this match and path")
		}
	}
	change.item.Routes = append(change.item.Routes, rule)
	return workerRoute(change.item.Host, rule), nil
}

func (s *Server) cfZoneWorkerRoutes(st *domain.State, p authorization.Principal, r *http.Request) (any, int, error) {
	zone, err := s.cfHostnameZone(st, p, r)
	if err != nil {
		return nil, 0, err
	}
	items, err := s.Store.ManagedHostnames(r.Context(), r.PathValue("org"))
	if err != nil {
		return nil, 0, err
	}
	id := r.PathValue("route_id")
	var found domain.ManagedHostname
	index := -1
	list := []cfWorkerRoute{}
	for _, item := range items {
		if item.SuffixID != zone.Suffix.ID {
			continue
		}
		for i, rule := range item.Routes {
			if rule.Origin != "" || rule.CustomDomain || rule.WorkerLocator == "" {
				continue
			}
			result := workerRoute(item.Host, rule)
			list = append(list, result)
			if result.ID == id {
				found, index = item, i
			}
		}
	}
	if id != "" && index < 0 {
		return nil, 0, domain.NotFound()
	}
	if r.Method == http.MethodGet {
		if index >= 0 {
			return workerRoute(found.Host, found.Routes[index]), http.StatusOK, nil
		}
		return list, http.StatusOK, nil
	}

	// Zone endpoints start with the actor's account list. Re-open just the
	// selected organization with its Worker identities for authorization.
	return s.withHostnameTargets(r, p, func(st *domain.State) (any, int, error) {
		changes, err := s.authorizeHostnameChanges(st, p, r)
		if err != nil {
			return nil, 0, err
		}
		if index >= 0 {
			if err := changes.authorizeWorker(found.Routes[index].Worker); err != nil {
				return nil, 0, err
			}
			change := changes.edit(found)
			change.item.Routes = slices.Delete(change.item.Routes, index, index+1)
		}
		var result any = map[string]string{"id": id}
		if r.Method != http.MethodDelete {
			var input cfRouteInput
			if err = s.body(r, &input); err != nil {
				return nil, 0, err
			}
			if input.ZoneID != "" && input.ZoneID != zone.ID || input.ZoneName != "" && input.ZoneName != zone.Name {
				return nil, 0, domain.Invalid("HOSTNAME_INVALID", "The route must use the selected zone")
			}
			result, err = changes.addWorkerRoute(input, zone)
			if err != nil {
				return nil, 0, err
			}
		}
		return result, http.StatusOK, changes.save()
	})
}

func (c *hostnameChanges) authorizeWorker(id string) error {
	ref := workerapp.WorkerRef{Organization: c.request.PathValue("org"), Worker: id}
	_, err := c.app.WorkerTarget(c.state, ref, "production", "worker.update", c.principal)
	return err
}

func (s *Server) cfScriptWorkerRoutes(st *domain.State, p authorization.Principal, r *http.Request) (any, int, error) {
	var input []cfRouteInput
	if r.Method == http.MethodPut {
		if err := s.body(r, &input); err != nil {
			return nil, 0, err
		}
		if input == nil || len(input) > 100 {
			return nil, 0, domain.Invalid("HOSTNAME_ROUTE_INVALID", "Supply at most 100 Worker routes")
		}
	}
	ref := workerRef(r)
	if r.Method == http.MethodGet {
		target, err := s.OrgTarget(st, r)
		if err != nil {
			return nil, 0, err
		}
		if err = s.Require(st, p, r, "worker.hostname.read", target); err != nil {
			return nil, 0, err
		}
		if _, err = s.WorkerTarget(st, r, "production", "worker.read", p); err != nil {
			return nil, 0, err
		}
		items, err := s.Store.WorkerManagedHostnames(r.Context(), ref.Organization, ref.Worker)
		if err != nil {
			return nil, 0, err
		}
		result := []cfWorkerRoute{}
		for _, item := range items {
			suffix, ok := s.Config.Workers.Domain(item.SuffixID)
			if !ok || suffix.Type != appconfig.WorkerDomainApplication {
				continue
			}
			for _, rule := range item.Routes {
				if !rule.CustomDomain {
					result = append(result, workerRoute(item.Host, rule))
				}
			}
		}
		return result, http.StatusOK, nil
	}
	return retryHostnameChanges(func() (any, int, error) {
		return s.replaceScriptWorkerRoutes(st, p, r, ref, input)
	})
}

// Wrangler submits Routes and Custom Domains in parallel. Rebuild the edit
// from current rows if another batch committed a route on the same host.
func retryHostnameChanges(fn func() (any, int, error)) (out any, status int, err error) {
	for attempt := 0; attempt < 4; attempt++ {
		out, status, err = fn()
		if !isProblem(err, "REVISION_CONFLICT") && !isProblem(err, "HOSTNAME_UNAVAILABLE") {
			break
		}
	}
	return out, status, err
}

func (s *Server) replaceScriptWorkerRoutes(st *domain.State, p authorization.Principal, r *http.Request, ref workerapp.WorkerRef, input []cfRouteInput) (any, int, error) {
	changes, err := s.authorizeHostnameChanges(st, p, r)
	if err != nil {
		return nil, 0, err
	}
	if err := changes.authorizeWorker(ref.Worker); err != nil {
		return nil, 0, err
	}
	items, err := s.Store.WorkerManagedHostnames(r.Context(), ref.Organization, ref.Worker)
	if err != nil {
		return nil, 0, err
	}
	result := []cfWorkerRoute{}
	for _, subset := range items {
		suffix, ok := s.Config.Workers.Domain(subset.SuffixID)
		if !ok || suffix.Type != appconfig.WorkerDomainApplication {
			continue
		}
		if !slices.ContainsFunc(subset.Routes, func(rule domain.ManagedHostnameRoute) bool {
			return !rule.CustomDomain && rule.CFPattern != ""
		}) {
			continue
		}
		item, err := s.Store.ManagedHostname(r.Context(), ref.Organization, subset.Host)
		if err != nil {
			return nil, 0, err
		}
		change := changes.edit(item)
		change.item.Routes = slices.DeleteFunc(change.item.Routes, func(rule domain.ManagedHostnameRoute) bool {
			return rule.Worker == ref.Worker && !rule.CustomDomain && rule.CFPattern != ""
		})
	}
	for _, entry := range input {
		entry.Script = r.PathValue("script")
		host, _, _ := strings.Cut(entry.Pattern, "/")
		zone, err := s.routeZone(ref.Organization, host, entry.ZoneID, entry.ZoneName)
		if err != nil {
			return nil, 0, err
		}
		route, err := changes.addWorkerRoute(entry, zone)
		if err != nil {
			return nil, 0, err
		}
		result = append(result, route)
	}
	return result, http.StatusOK, changes.save()
}
