package control

import (
	"context"

	appconfig "github.com/krun-dev/krun/internal/config"
)

func (s *Server) repositoryCredential(ctx context.Context, provider appconfig.WorkerRepositoryProvider, userID string) (string, error) {
	return s.repositoryApplication().RepositoryCredential(ctx, provider, userID)
}

func (s *Server) readRepositoryConnection(ctx context.Context, user, provider string) (WorkerRepositoryConnection, bool, error) {
	return s.repositoryApplication().ReadRepositoryConnection(ctx, user, provider)
}

func (s *Server) claimRepositoryCredentialRefresh(ctx context.Context, connection WorkerRepositoryConnection) (WorkerRepositoryConnection, bool, error) {
	return s.repositoryApplication().ClaimRepositoryCredentialRefresh(ctx, connection)
}

func (s *Server) completeRepositoryCredentialRefresh(ctx context.Context, connection WorkerRepositoryConnection, claimRevision int64) (bool, error) {
	return s.repositoryApplication().CompleteRepositoryCredentialRefresh(ctx, connection, claimRevision)
}

func (s *Server) failRepositoryCredentialRefresh(ctx context.Context, connection WorkerRepositoryConnection, claimRevision int64, reconnect bool) error {
	return s.repositoryApplication().FailRepositoryCredentialRefresh(ctx, connection, claimRevision, reconnect)
}

func (s *Server) settleRepositoryCredentialRefresh(connection WorkerRepositoryConnection, claimRevision int64, reconnect bool) error {
	return s.repositoryApplication().SettleRepositoryCredentialRefresh(connection, claimRevision, reconnect)
}

func (s *Server) commitRepositoryCredentialRefresh(connection WorkerRepositoryConnection, claimRevision int64) (bool, error) {
	return s.repositoryApplication().CommitRepositoryCredentialRefresh(connection, claimRevision)
}

func (s *Server) refreshRepositoryCredential(ctx context.Context, provider appconfig.WorkerRepositoryProvider, initial WorkerRepositoryConnection) (string, error) {
	return s.repositoryApplication().RefreshRepositoryCredential(ctx, provider, initial)
}

func (s *Server) refreshClaimedRepositoryCredential(ctx context.Context, provider appconfig.WorkerRepositoryProvider, claimed WorkerRepositoryConnection) (string, error) {
	return s.repositoryApplication().RefreshClaimedRepositoryCredential(ctx, provider, claimed)
}

func (s *Server) resolveRepositoryCommit(ctx context.Context, provider appconfig.WorkerRepositoryProvider, source WorkerRepositorySource, token string) (string, error) {
	return s.repositoryApplication().ResolveRepositoryCommit(ctx, provider, source, token)
}

func (s *Server) fetchRepositorySource(ctx context.Context, provider appconfig.WorkerRepositoryProvider, source WorkerBuildRepository, token string) (map[string]string, []byte, error) {
	return s.repositoryApplication().FetchRepositorySource(ctx, provider, source, token)
}
