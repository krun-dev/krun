package control

func (s *Server) syncEnvironmentHosts(env *WorkerEnvironment, organization, service string, workerDomains []string) {
	s.workerApplication(nil).SyncEnvironmentHosts(env, organization, service, workerDomains)
}
