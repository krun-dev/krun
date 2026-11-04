package control

func (s *Server) syncEnvironmentHosts(env *WorkerEnvironment, organization, service string, serviceDomains []string) {
	s.workerApplication(nil).SyncEnvironmentHosts(env, organization, service, serviceDomains)
}
