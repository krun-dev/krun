package deployment

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"

	"github.com/krun-dev/krun/internal/config"
)

func gatewayPort(g config.WorkerGateway) int {
	if g.Base != "" {
		u, _ := url.Parse(g.Base)
		if value, err := strconv.Atoi(u.Port()); err == nil {
			return value
		}
		return 80
	}
	return 8080
}

// CheckGateway validates a kubectl JSON response without opening cluster access.
// Gateway, TLS termination and LB exposure remain company-managed resources.
func CheckGateway(c config.Config, raw []byte) error {
	if err := validate(c); err != nil {
		return err
	}
	var gateway struct {
		Metadata struct{ Name, Namespace string }
		Spec     struct {
			Listeners []struct {
				Name, Protocol, Hostname string
				Port                     int
				AllowedRoutes            struct {
					Namespaces struct {
						From     string
						Selector struct {
							MatchLabels      map[string]string
							MatchExpressions []any
						}
					}
					Kinds []struct{ Group, Kind string }
				}
			}
		}
		Status struct {
			Conditions []struct{ Type, Status string }
		}
	}
	if err := json.Unmarshal(raw, &gateway); err != nil {
		return errors.New("invalid Gateway JSON")
	}
	var expected config.WorkerGateway
	for _, candidate := range c.Workers.Gateways {
		if gateway.Metadata.Name == candidate.Name && gateway.Metadata.Namespace == candidate.Namespace {
			expected = candidate
			break
		}
	}
	if gateway.Metadata.Name != expected.Name || gateway.Metadata.Namespace != expected.Namespace {
		return errors.New("Gateway identity does not match main configuration")
	}
	for _, listener := range gateway.Spec.Listeners {
		if listener.Name != expected.Section {
			continue
		}
		if listener.Protocol != "HTTP" || listener.Port != gatewayPort(expected) || listener.Hostname != "" {
			return errors.New("Gateway listener must be HTTP on the configured internal port, without a hostname restriction; company edge terminates HTTPS")
		}
		if len(listener.AllowedRoutes.Kinds) > 0 {
			allowed := false
			for _, kind := range listener.AllowedRoutes.Kinds {
				allowed = allowed || kind.Kind == "HTTPRoute" && (kind.Group == "" || kind.Group == "gateway.networking.k8s.io")
			}
			if !allowed {
				return errors.New("Gateway listener does not allow HTTPRoute")
			}
		}
		ns := listener.AllowedRoutes.Namespaces
		switch ns.From {
		case "All":
		case "", "Same":
			if expected.Namespace != c.Workers.Namespace {
				return errors.New("Gateway must allow routes from the application namespace")
			}
		case "Selector":
			if len(ns.Selector.MatchExpressions) > 0 {
				return errors.New("use matchLabels for the dedicated krun namespace allowance")
			}
			labels := map[string]string{
				"kubernetes.io/metadata.name": c.Workers.Namespace,
				"app.kubernetes.io/part-of":   "krun",
			}
			for key, value := range ns.Selector.MatchLabels {
				if labels[key] != value {
					return errors.New("Gateway namespace selector does not include the rendered namespace")
				}
			}
		default:
			return errors.New("unsupported Gateway namespace allowance")
		}
		for _, condition := range gateway.Status.Conditions {
			if condition.Type == "Programmed" && condition.Status == "True" {
				return nil
			}
		}
		return errors.New("Gateway is not Programmed=True")
	}
	return fmt.Errorf("Gateway listener %q not found", expected.Section)
}

func consoleGateway(c config.Config) config.WorkerGateway {
	for _, g := range c.Workers.Gateways {
		if g.ID == c.Deployment.ConsoleGateway || (c.Deployment.ConsoleGateway == "" && len(c.Workers.Gateways) == 1) {
			return g
		}
	}
	return config.WorkerGateway{}
}

// CheckGateways requires a complete list, so a missing second or third gateway
// cannot silently pass deployment preflight.
func CheckGateways(c config.Config, raw []byte) error {
	if err := validate(c); err != nil {
		return err
	}
	var list struct{ Items []json.RawMessage }
	if err := json.Unmarshal(raw, &list); err != nil {
		return err
	}
	if len(list.Items) != len(c.Workers.Gateways) {
		return errors.New("Gateway list must contain every configured Gateway exactly once")
	}
	seen := map[string]bool{}
	for _, item := range list.Items {
		var identity struct {
			Metadata struct{ Name, Namespace string }
		}
		if err := json.Unmarshal(item, &identity); err != nil {
			return err
		}
		key := identity.Metadata.Namespace + "/" + identity.Metadata.Name
		if seen[key] {
			return errors.New("duplicate Gateway in preflight list")
		}
		seen[key] = true
		if err := CheckGateway(c, item); err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
	}
	return nil
}
