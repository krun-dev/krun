package cfcompat

import (
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/krun-dev/krun/internal/authorization"
	appconfig "github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/securetoken"
)

const originPhase = "http_request_origin"

type cfOriginRule struct {
	ID          string             `json:"id,omitempty"`
	Action      string             `json:"action"`
	Expression  string             `json:"expression"`
	Description string             `json:"description,omitempty"`
	Enabled     *bool              `json:"enabled,omitempty"`
	Parameters  cfOriginParameters `json:"action_parameters"`
}

type cfOriginParameters struct {
	HostHeader string `json:"host_header,omitempty"`
	Origin     struct {
		Host string `json:"host"`
		Port int    `json:"port,omitempty"`
	} `json:"origin"`
}

type cfOriginRuleset struct {
	ID          string         `json:"id,omitempty"`
	Name        string         `json:"name,omitempty"`
	Description string         `json:"description,omitempty"`
	Kind        string         `json:"kind,omitempty"`
	Phase       string         `json:"phase,omitempty"`
	Rules       []cfOriginRule `json:"rules"`
}

var originHostExpression = regexp.MustCompile(`^http\.host\s+eq\s+"([a-zA-Z0-9.-]+)"(?:\s+and\s+(.+))?$`)
var originExactExpression = regexp.MustCompile(`^http\.request\.uri\.path\s+eq\s+"(/[^"]*)"$`)
var originSegmentExpression = regexp.MustCompile(`^\(http\.request\.uri\.path\s+eq\s+"(/[^"]*)"\s+or\s+starts_with\(http\.request\.uri\.path,\s*"(/[^"]*/)"\)\)$`)

func parseOriginExpression(expression string) (host, match, path string, err error) {
	expression = strings.TrimSpace(expression)
	if strings.HasPrefix(expression, "(") && strings.HasSuffix(expression, ")") &&
		originHostExpression.MatchString(expression[1:len(expression)-1]) {
		expression = expression[1 : len(expression)-1]
	}
	found := originHostExpression.FindStringSubmatch(expression)
	if found != nil {
		host = strings.ToLower(found[1])
		switch {
		case found[2] == "":
			return host, "prefix", "/", nil
		case originExactExpression.MatchString(found[2]):
			return host, "exact", originExactExpression.FindStringSubmatch(found[2])[1], nil
		case found[2] == `starts_with(http.request.uri.path, "/")`:
			return host, "prefix", "/", nil
		default:
			if segment := originSegmentExpression.FindStringSubmatch(found[2]); segment != nil && segment[1]+"/" == segment[2] {
				return host, "prefix", segment[1], nil
			}
		}
	}
	return "", "", "", domain.Invalid("HOSTNAME_ROUTE_INVALID", "Use http.host eq with an optional exact path or a path-segment prefix expression")
}

func (s *Server) originRuleset(items []domain.ManagedHostname, zone hostnameZone) cfOriginRuleset {
	result := cfOriginRuleset{
		ID: securetoken.Digest(zone.ID + "/" + originPhase)[:32], Name: "Origin Rules",
		Kind: "zone", Phase: originPhase, Rules: []cfOriginRule{},
	}
	for _, item := range items {
		if item.SuffixID != zone.Suffix.ID || item.Type != string(appconfig.WorkerDomainApplication) || !zone.Suffix.Allows(appconfig.WorkerDomainApplication) {
			continue
		}
		for _, rule := range item.Routes {
			if rule.Origin == "" {
				continue
			}
			expression := fmt.Sprintf("http.host eq %q", item.Host)
			if rule.Match == "exact" {
				expression += fmt.Sprintf(" and http.request.uri.path eq %q", rule.Path)
			} else if rule.Path != "/" {
				expression += fmt.Sprintf(" and (http.request.uri.path eq %q or starts_with(http.request.uri.path, %q))", rule.Path, rule.Path+"/")
			}
			origin, _ := s.Config.Workers.ManagedOriginFor(item.Organization, item.SuffixID, rule.Origin)
			entry := cfOriginRule{
				ID: workerRoute(item.Host, rule).ID, Action: "route", Enabled: &rule.Enabled,
				Expression: expression, Parameters: cfOriginParameters{HostHeader: origin.HostHeader},
			}
			entry.Parameters.Origin.Host = rule.Origin
			entry.Parameters.Origin.Port = origin.Port
			result.Rules = append(result.Rules, entry)
		}
	}
	return result
}

func (s *Server) cfOriginRuleset(st *domain.State, p authorization.Principal, r *http.Request) (any, int, error) {
	zone, err := s.cfHostnameZone(st, p, r)
	if err != nil {
		return nil, 0, err
	}
	org := r.PathValue("org")
	items, err := s.Store.ManagedHostnames(r.Context(), org)
	if err != nil {
		return nil, 0, err
	}
	result := s.originRuleset(items, zone)
	if r.Method == http.MethodGet {
		return result, http.StatusOK, nil
	}
	if !zone.Suffix.Allows(appconfig.WorkerDomainApplication) {
		return nil, 0, domain.Invalid("HOSTNAME_INVALID", "Choose a suffix of type application")
	}
	return s.withHostnameTargets(r, p, func(st *domain.State) (any, int, error) {
		changes, err := s.authorizeHostnameChanges(st, p, r)
		if err != nil {
			return nil, 0, err
		}
		var input cfOriginRuleset
		if err := s.body(r, &input); err != nil {
			return nil, 0, err
		}
		if input.Rules == nil || len(input.Rules) > 100 || (input.Phase != "" && input.Phase != originPhase) ||
			(input.Kind != "" && input.Kind != "zone") {
			return nil, 0, domain.Invalid("HOSTNAME_ROUTE_INVALID", "Supply at most 100 zone Origin Rules")
		}
		for _, item := range items {
			if item.SuffixID == zone.Suffix.ID && item.Type == string(appconfig.WorkerDomainApplication) && slices.ContainsFunc(item.Routes, func(rule domain.ManagedHostnameRoute) bool { return rule.Origin != "" }) {
				change := changes.edit(item)
				change.item.Routes = slices.DeleteFunc(change.item.Routes, func(rule domain.ManagedHostnameRoute) bool { return rule.Origin != "" })
			}
		}
		for _, rule := range input.Rules {
			host, match, path, err := parseOriginExpression(rule.Expression)
			if err != nil {
				return nil, 0, err
			}
			change, err := changes.hostname(host, zone, appconfig.WorkerDomainApplication)
			if err != nil {
				return nil, 0, err
			}
			origin, ok := s.Config.Workers.ManagedOriginFor(org, zone.Suffix.ID, rule.Parameters.Origin.Host)
			if rule.Action != "route" || !ok || (rule.Parameters.Origin.Port != 0 && origin.Port != rule.Parameters.Origin.Port) ||
				(rule.Parameters.HostHeader != "" && rule.Parameters.HostHeader != origin.HostHeader) {
				return nil, 0, domain.Invalid("HOSTNAME_TARGET_INVALID", "Use a registered origin and its configured port and Host header")
			}
			valid, err := changes.app.ValidateManagedHostnameRoutes(r.Context(), st, p, org, zone.Suffix.ID,
				[]domain.ManagedHostnameRoute{{Match: match, Path: path, Origin: origin.Host, Enabled: rule.Enabled == nil || *rule.Enabled}}, nil)
			if err != nil {
				return nil, 0, err
			}
			candidate := valid[0]
			if slices.ContainsFunc(change.item.Routes, func(old domain.ManagedHostnameRoute) bool {
				return old.Match == candidate.Match && old.Path == candidate.Path
			}) {
				return nil, 0, domain.Conflict("HOSTNAME_ROUTE_CONFLICT", "A route already uses this match and path")
			}
			change.item.Routes = append(change.item.Routes, candidate)
		}
		if err := changes.save(); err != nil {
			return nil, 0, err
		}
		items, err = s.Store.ManagedHostnames(r.Context(), org)
		return s.originRuleset(items, zone), http.StatusOK, err
	})
}
