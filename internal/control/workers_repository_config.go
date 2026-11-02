package control

import (
	appconfig "github.com/krun-dev/krun/internal/config"
)

func (s *Server) repositoryProvider(id, organization string) (appconfig.WorkerRepositoryProvider, error) {
	return s.repositoryApplication().RepositoryProvider(id, organization)
}

func (s *Server) validateRepositorySource(source *WorkerRepositorySource, organization string) (appconfig.WorkerRepositoryProvider, error) {
	return s.repositoryApplication().ValidateRepositorySource(source, organization)
}
