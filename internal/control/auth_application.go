package control

import authapp "github.com/krun-dev/krun/internal/auth"

func (s *Server) authApplication() *authapp.Service {
	return &authapp.Service{Store: s.Store, SourceEnabled: s.sourceEnabled}
}
