package control

import (
	"context"

	appconfig "github.com/krun-dev/krun/internal/config"
)

func (s *Server) repositoryCredential(ctx context.Context, provider appconfig.WorkerRepositoryProvider, userID string) (string, error) {
	return s.repositoryApplication().RepositoryCredential(ctx, provider, userID)
}

func (s *Server) resolveRepositoryCommit(ctx context.Context, provider appconfig.WorkerRepositoryProvider, source WorkerRepositorySource, token string) (string, error) {
	return s.repositoryApplication().ResolveRepositoryCommit(ctx, provider, source, token)
}

func (s *Server) fetchRepositorySource(ctx context.Context, provider appconfig.WorkerRepositoryProvider, source WorkerBuildRepository, token string) (map[string]string, error) {
	return s.repositoryApplication().FetchRepositorySource(ctx, provider, source, token)
}
