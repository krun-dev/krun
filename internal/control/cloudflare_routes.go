package control

import (
	"errors"
	"net/http"
	"strings"

	"github.com/krun-dev/krun/internal/adapters/cfcompat"
	"github.com/krun-dev/krun/internal/domain"
)

const cfPrefix = cfcompat.Prefix

func isProblem(err error, code string) bool {
	var problem *domain.APIError
	return errors.As(err, &problem) && problem.Code == code
}

// Cloudflare uses merge-patch JSON for Secret and Preview configuration updates.
func cfBody(r *http.Request, value any) error {
	copy := r.Clone(r.Context())
	copy.Header = r.Header.Clone()
	if strings.HasPrefix(copy.Header.Get("Content-Type"), "application/merge-patch+json") {
		copy.Header.Set("Content-Type", "application/json")
	}
	return body(copy, value)
}

func cfCredentials(c authCredentials) cfcompat.Credentials {
	return cfcompat.Credentials{Value: c, KeyID: c.keyID, HasSession: c.hasSession}
}

func controlCredentials(c cfcompat.Credentials) authCredentials {
	credentials, _ := c.Value.(authCredentials)
	return credentials
}

func (s *Server) cloudflareRoutes(m *recordingMux) {
	objects := func(st *State, p Principal, r *http.Request) (any, int, error) {
		return s.workerObjects(st, p, r)
	}
	for _, route := range s.cf.Routes(objects) {
		if route.AccountScope {
			m.useScope(scopeCloudflareAccounts)
		} else {
			m.useScope(scopeCloudflareOrganization)
		}
		m.HandleFunc(route.Pattern, route.Handler)
	}
}
