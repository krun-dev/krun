package control

import "github.com/krun-dev/krun/internal/runtimeconfig"

func (s *Server) runtimeApplication() *runtimeconfig.Service {
	application := &runtimeconfig.Service{Config: s.Config.Workers, Store: s.Store, Controller: s.workers}
	if s.storageCredentials != nil {
		application.Credentials = s.storageCredentials
	}
	return application
}
