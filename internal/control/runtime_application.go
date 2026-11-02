package control

import "github.com/krun-dev/krun/internal/runtimeconfig"

func (s *Server) runtimeApplication() *runtimeconfig.Service {
	return &runtimeconfig.Service{Config: s.Config.Workers, Store: s.Store, Controller: s.workers}
}
