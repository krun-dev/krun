package control

func (s *Server) syncEnvironmentHosts(env *WorkerEnvironment, organization, service string, serviceDomains []string, profiles ...string) {
	s.workerApplication(nil).SyncEnvironmentHosts(env, organization, service, serviceDomains, profiles...)
}

func (s *Server) syncServiceHosts(ws *WorkerService) []string {
	return s.workerApplication(nil).SyncServiceHosts(ws)
}

func (s *Server) syncServiceDomains(ws WorkerService) []string {
	return s.workerApplication(nil).SyncServiceDomains(ws)
}
