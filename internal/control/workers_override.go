package control

import (
	"net/http"
	"strings"

	workerapp "github.com/krun-dev/krun/internal/workers"
)

func (s *Server) workerRuntimeAuthorizeOverride(w http.ResponseWriter, r *http.Request) {
	pool, allowed := s.workerRuntimeServingPool(r)
	if !allowed {
		s.writeError(w, r, forbidden())
		return
	}

	var in struct {
		Host       string `json:"host"`
		Credential string `json:"credential"`
	}
	if err := bodyWithLimit(r, &in, 4096); err != nil {
		s.writeError(w, r, err)
		return
	}
	if !strings.HasPrefix(in.Credential, "Bearer ") || len(in.Credential) > 2048 {
		s.writeError(w, r, forbidden())
		return
	}
	request := r.Clone(r.Context())
	request.Header = http.Header{"Authorization": []string{in.Credential}}
	credentials, err := s.readCredentials(request)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if !credentials.hasSession && credentials.keyID == "" {
		s.writeError(w, r, forbidden())
		return
	}

	route, err := s.runtimeApplication().Route(r.Context(), in.Host, pool.ID)
	if err != nil || len(route.Routes) != 1 {
		s.writeError(w, r, forbidden())
		return
	}
	target := route.Routes[0]
	if !target.Enabled || target.Expires != nil || target.Environment == "" {
		s.writeError(w, r, forbidden())
		return
	}
	version, ok := route.Versions[target.Version]
	if !ok {
		s.writeError(w, r, forbidden())
		return
	}

	owner := credentials.session.User
	if credentials.isKey {
		var found bool
		owner, found, err = s.Store.APIKeyOwner(r.Context(), credentials.keyID)
		if err != nil || !found {
			s.writeError(w, r, forbidden())
			return
		}
	}
	ctx := withStateScope(r.Context(), stateReadScope{
		User: owner, Key: credentials.keyID, Organization: version.Organization,
		Worker: target.Worker, Kind: "worker", Limit: 1, Summary: true,
	})
	err = s.Store.View(ctx, PartitionIAM, func(st *State) error {
		principal, err := s.authenticate(st, credentials, request)
		if err != nil {
			return forbidden()
		}
		if credentials.session.OAuthGrant != "" &&
			!strings.Contains(" "+credentials.session.OAuthScope+" ", " krun:control ") &&
			!strings.Contains(" "+credentials.session.OAuthScope+" ", " workers:write ") &&
			!strings.Contains(" "+credentials.session.OAuthScope+" ", " workers_scripts:write ") {
			return forbidden()
		}
		ref := workerapp.WorkerRef{Organization: version.Organization, Worker: target.Worker}
		_, err = s.workerApplication(request).WorkerTarget(st, ref, target.Environment, "worker.publish", principal)
		return err
	})
	if err != nil {
		s.writeError(w, r, forbidden())
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	s.json(w, 200, map[string]bool{"allowed": true})
}
