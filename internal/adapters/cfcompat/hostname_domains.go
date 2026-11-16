package cfcompat

import (
	"net/http"
	"slices"
	"strings"

	"github.com/krun-dev/krun/internal/authorization"
	appconfig "github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/securetoken"
)

type cfDomainInput struct {
	Hostname        string `json:"hostname"`
	Service         string `json:"service,omitempty"`
	Environment     string `json:"environment,omitempty"`
	ZoneID          string `json:"zone_id,omitempty"`
	ZoneName        string `json:"zone_name,omitempty"`
	Enabled         *bool  `json:"enabled,omitempty"`
	PreviewsEnabled *bool  `json:"previews_enabled,omitempty"`
}

type cfDomainRecord struct {
	ID              string `json:"id"`
	Hostname        string `json:"hostname"`
	Service         string `json:"service"`
	Environment     string `json:"environment"`
	ZoneID          string `json:"zone_id"`
	ZoneName        string `json:"zone_name"`
	Enabled         bool   `json:"enabled"`
	PreviewsEnabled bool   `json:"previews_enabled"`
}

func (s *Server) domainRecord(item domain.ManagedHostname, rule domain.ManagedHostnameRoute) cfDomainRecord {
	suffix, _ := s.Config.Workers.Domain(item.SuffixID)
	return cfDomainRecord{
		ID: securetoken.Digest("domain\x00" + item.Host)[:32], Hostname: item.Host,
		Service: rule.WorkerLocator, Environment: "production",
		ZoneID: zoneID(item.Organization, item.SuffixID), ZoneName: suffix.Domain, Enabled: rule.Enabled, PreviewsEnabled: rule.PreviewsEnabled,
	}
}

func (s *Server) addCustomDomain(changes *hostnameChanges, in cfDomainInput, override bool) (cfDomainRecord, error) {
	if in.Service == "" || (in.Environment != "" && in.Environment != "production") {
		return cfDomainRecord{}, domain.Invalid("HOSTNAME_TARGET_INVALID", "Custom Domains require a production Worker")
	}
	org := changes.request.PathValue("org")
	zone, err := s.routeZone(org, in.Hostname, in.ZoneID, in.ZoneName)
	if err != nil {
		return cfDomainRecord{}, err
	}
	change, err := changes.hostname(in.Hostname, zone, appconfig.WorkerDomainCustom)
	if err != nil {
		return cfDomainRecord{}, err
	}
	enabled := in.Enabled == nil || *in.Enabled
	rules, err := changes.app.ValidateManagedHostnameRoutes(changes.request.Context(), changes.state, changes.principal,
		org, zone.Suffix.ID, []domain.ManagedHostnameRoute{{Match: "prefix", Path: "/", Worker: in.Service, Enabled: enabled}}, nil)
	if err != nil {
		return cfDomainRecord{}, err
	}
	rule := rules[0]
	rule.CustomDomain = true
	rule.PreviewsEnabled = in.PreviewsEnabled == nil || *in.PreviewsEnabled
	for i, old := range change.item.Routes {
		if len(change.item.Routes) != 1 || old.Match != "prefix" || old.Path != "/" ||
			!old.CustomDomain || (!override && old.Worker != rule.Worker) {
			return cfDomainRecord{}, domain.Conflict("HOSTNAME_ROUTE_CONFLICT", "A route already uses this match and path")
		}
		if old.Worker != rule.Worker {
			if err := changes.authorizeWorker(old.Worker); err != nil {
				return cfDomainRecord{}, err
			}
		}
		change.item.Routes[i] = rule
		return s.domainRecord(change.item, rule), nil
	}
	change.item.Routes = append(change.item.Routes, rule)
	return s.domainRecord(change.item, rule), nil
}

func (s *Server) cfCustomDomains(st *domain.State, p authorization.Principal, r *http.Request) (any, int, error) {
	target, err := s.OrgTarget(st, r)
	if err != nil {
		return nil, 0, err
	}
	if r.Method == http.MethodGet {
		if err := s.Require(st, p, r, "worker.hostname.read", target); err != nil {
			return nil, 0, err
		}
	}
	if r.Method == http.MethodPut {
		changes, err := s.authorizeHostnameChanges(st, p, r)
		if err != nil {
			return nil, 0, err
		}
		var input cfDomainInput
		if err := s.body(r, &input); err != nil {
			return nil, 0, err
		}
		record, err := s.addCustomDomain(changes, input, true)
		if err != nil {
			return nil, 0, err
		}
		return record, http.StatusOK, changes.save()
	}

	items, err := s.Store.ManagedHostnames(r.Context(), target.ID)
	if err != nil {
		return nil, 0, err
	}
	id := r.PathValue("domain_id")
	query := r.URL.Query()
	list := []cfDomainRecord{}
	for _, item := range items {
		suffix, ok := s.Config.Workers.Domain(item.SuffixID)
		if !ok || !suffix.Allows(appconfig.WorkerDomainCustom) || item.Type != string(appconfig.WorkerDomainCustom) {
			continue
		}
		for i, rule := range item.Routes {
			if !rule.CustomDomain || rule.WorkerLocator == "" {
				continue
			}
			record := s.domainRecord(item, rule)
			if id != "" {
				if record.ID != id {
					continue
				}
				if r.Method == http.MethodDelete {
					changes, err := s.authorizeHostnameChanges(st, p, r)
					if err != nil {
						return nil, 0, err
					}
					if err := changes.authorizeWorker(rule.Worker); err != nil {
						return nil, 0, err
					}
					change := changes.edit(item)
					change.item.Routes = slices.Delete(change.item.Routes, i, i+1)
					return map[string]string{"id": id}, http.StatusOK, changes.save()
				}
				return record, http.StatusOK, nil
			}
			if matchesFilter(query.Get("hostname"), record.Hostname) &&
				matchesFilter(query.Get("service"), record.Service) &&
				matchesFilter(query.Get("environment"), record.Environment) &&
				matchesFilter(query.Get("zone_id"), record.ZoneID) &&
				matchesFilter(query.Get("zone_name"), record.ZoneName) {
				list = append(list, record)
			}
		}
	}
	if id != "" {
		return nil, 0, domain.NotFound()
	}
	return list, http.StatusOK, nil
}

func matchesFilter(filter, actual string) bool { return filter == "" || filter == actual }

func (s *Server) cfScriptCustomDomains(st *domain.State, p authorization.Principal, r *http.Request) (any, int, error) {
	var err error
	var input struct {
		Origins        []cfDomainInput `json:"origins"`
		OverrideScope  bool            `json:"override_scope"`
		OverrideOrigin bool            `json:"override_existing_origin"`
		OverrideDNS    bool            `json:"override_existing_dns_record"`
	}
	dry := r.Method == http.MethodPost
	if dry {
		err = s.body(r, &input.Origins)
	} else {
		err = s.body(r, &input)
	}
	if err != nil {
		return nil, 0, err
	}
	if input.Origins == nil || len(input.Origins) > 100 {
		return nil, 0, domain.Invalid("HOSTNAME_ROUTE_INVALID", "Supply at most 100 Custom Domains")
	}
	return retryHostnameChanges(func() (any, int, error) {
		return s.replaceScriptCustomDomains(st, p, r, input.Origins, dry, input.OverrideScope, input.OverrideOrigin)
	})
}

func (s *Server) replaceScriptCustomDomains(st *domain.State, p authorization.Principal, r *http.Request, origins []cfDomainInput, dry, replaceScope, overrideOrigin bool) (any, int, error) {
	changes, err := s.authorizeHostnameChanges(st, p, r)
	if err != nil {
		return nil, 0, err
	}
	ref := workerRef(r)
	if err := changes.authorizeWorker(ref.Worker); err != nil {
		return nil, 0, err
	}
	items, err := s.Store.ManagedHostnames(r.Context(), ref.Organization)
	if err != nil {
		return nil, 0, err
	}
	seen := map[string]bool{}
	result := []cfDomainRecord{}
	added := []cfDomainRecord{}
	updated := []map[string]any{}
	removed := []cfDomainRecord{}
	existing := map[string]cfDomainRecord{}
	for _, item := range items {
		suffix, ok := s.Config.Workers.Domain(item.SuffixID)
		if !ok || !suffix.Allows(appconfig.WorkerDomainCustom) || item.Type != string(appconfig.WorkerDomainCustom) {
			continue
		}
		for _, rule := range item.Routes {
			if rule.CustomDomain {
				existing[item.Host] = s.domainRecord(item, rule)
			}
		}
	}
	for _, entry := range origins {
		entry.Hostname = strings.ToLower(entry.Hostname)
		if seen[entry.Hostname] {
			return nil, 0, domain.Invalid("HOSTNAME_ROUTE_INVALID", "A Custom Domain appears more than once")
		}
		seen[entry.Hostname] = true
		entry.Service = r.PathValue("script")
		record, err := s.addCustomDomain(changes, entry, dry || overrideOrigin)
		if err != nil {
			return nil, 0, err
		}
		result = append(result, record)
		if old, ok := existing[record.Hostname]; ok {
			updated = append(updated, map[string]any{"id": old.ID, "hostname": old.Hostname, "modified": old != record, "previews_enabled": record.PreviewsEnabled})
		} else {
			added = append(added, record)
		}
	}
	if dry || replaceScope {
		for _, item := range items {
			suffix, ok := s.Config.Workers.Domain(item.SuffixID)
			if seen[item.Host] || !ok || !suffix.Allows(appconfig.WorkerDomainCustom) || item.Type != string(appconfig.WorkerDomainCustom) {
				continue
			}
			for i, rule := range item.Routes {
				if rule.CustomDomain && rule.Worker == ref.Worker {
					removed = append(removed, s.domainRecord(item, rule))
					if !dry {
						change := changes.edit(item)
						change.item.Routes = slices.Delete(change.item.Routes, i, i+1)
					}
					break
				}
			}
		}
	}
	if dry {
		return map[string]any{"added": added, "updated": updated, "removed": removed, "conflicting": []any{}}, http.StatusOK, nil
	}
	return result, http.StatusOK, changes.save()
}
