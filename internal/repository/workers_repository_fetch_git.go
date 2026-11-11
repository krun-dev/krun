package repository

import (
	"context"
	"fmt"
	"time"

	gitadapter "github.com/krun-dev/krun/internal/adapters/gitrepository"
	repooauth "github.com/krun-dev/krun/internal/adapters/repositoryoauth"
	appconfig "github.com/krun-dev/krun/internal/config"
	domain "github.com/krun-dev/krun/internal/domain"
)

// RepositoryCredential is used by ordinary bindings, including public and
// administrator-configured repository sources.
func (s *Service) RepositoryCredential(ctx context.Context, provider appconfig.WorkerRepositoryProvider, userID string) (string, error) {
	if userID == "" {
		return provider.Token, nil
	}
	grant, ok, err := s.userRepositoryGrant(ctx, provider, userID)
	if err != nil {
		return "", err
	}
	if !ok {
		return provider.Token, nil
	}
	return grant.AccessToken, nil
}

// BoundRepositoryCredential cannot switch accounts or fall back to the
// administrator token after a user explicitly binds their own authorization.
func (s *Service) BoundRepositoryCredential(ctx context.Context, provider appconfig.WorkerRepositoryProvider, userID string) (string, error) {
	if provider.ClientID == "" {
		return "", reconnectRequired()
	}
	grant, ok, err := s.userRepositoryGrant(ctx, provider, userID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", reconnectRequired()
	}
	return grant.AccessToken, nil
}

func reconnectRequired() error {
	return domain.Conflict("WORKER_REPOSITORY_RECONNECT_REQUIRED", "Repository authorization expired or was removed; reconnect the account")
}

func (s *Service) userRepositoryGrant(ctx context.Context, provider appconfig.WorkerRepositoryProvider, userID string) (domain.WorkerRepositoryConnection, bool, error) {
	grant, ok, err := s.ReadRepositoryConnection(ctx, userID, authorizationKey(provider))
	if err != nil || !ok {
		return grant, ok, err
	}
	if grant.ReconnectRequired || grant.AccessToken == "" {
		return grant, true, reconnectRequired()
	}
	now := time.Now().UTC()
	if grant.ExpiresAt.IsZero() || grant.ExpiresAt.After(now.Add(time.Minute)) ||
		(grant.RefreshToken == "" && grant.ExpiresAt.After(now)) {
		return grant, true, nil
	}
	token, err := s.RefreshRepositoryCredential(ctx, provider, grant)
	if err != nil {
		return grant, true, err
	}
	grant, ok, err = s.ReadRepositoryConnection(ctx, userID, authorizationKey(provider))
	if err != nil {
		return grant, ok, err
	}
	if !ok || grant.AccessToken != token || grant.ReconnectRequired {
		return grant, true, domain.Conflict("WORKER_REPOSITORY_AUTHORIZATION_CHANGED", "Repository authorization changed; retry the request")
	}
	return grant, true, nil
}

func (s *Service) ReadRepositoryConnection(ctx context.Context, user, provider string) (domain.WorkerRepositoryConnection, bool, error) {
	return s.Store.RepositoryConnection(ctx, user, provider)
}

func (s *Service) ClaimRepositoryCredentialRefresh(ctx context.Context, connection domain.WorkerRepositoryConnection) (domain.WorkerRepositoryConnection, bool, error) {
	expected := connection.TokenRevision
	if expected == 0 {
		expected = 1
	}
	return s.Store.ClaimRepositoryConnectionRefresh(ctx, connection.User, connection.Provider, expected)
}

func (s *Service) CompleteRepositoryCredentialRefresh(ctx context.Context, connection domain.WorkerRepositoryConnection, claimRevision int64) (bool, error) {
	return s.Store.CompleteRepositoryConnectionRefresh(ctx, connection, claimRevision)
}

func (s *Service) FailRepositoryCredentialRefresh(ctx context.Context, connection domain.WorkerRepositoryConnection, claimRevision int64, reconnect bool) error {
	_, err := s.Store.FailRepositoryConnectionRefresh(ctx, connection.User, connection.Provider, claimRevision, reconnect)
	return err
}

// A provider may rotate the refresh token before the build request context is
// cancelled. Finalize or version the durable lease with a short independent
// context so cancellation cannot strand or lose the newly issued grant.
func (s *Service) SettleRepositoryCredentialRefresh(connection domain.WorkerRepositoryConnection, claimRevision int64, reconnect bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return s.FailRepositoryCredentialRefresh(ctx, connection, claimRevision, reconnect)
}

func (s *Service) CommitRepositoryCredentialRefresh(connection domain.WorkerRepositoryConnection, claimRevision int64) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return s.CompleteRepositoryCredentialRefresh(ctx, connection, claimRevision)
}

func (s *Service) RefreshRepositoryCredential(ctx context.Context, provider appconfig.WorkerRepositoryProvider, initial domain.WorkerRepositoryConnection) (string, error) {
	connection := initial
	waitUntil := time.Now().UTC().Add(10 * time.Second)
	for {
		if connection.ReconnectRequired || connection.AccessToken == "" {
			return "", reconnectRequired()
		}
		if connection.ExpiresAt.IsZero() || connection.ExpiresAt.After(time.Now().UTC().Add(time.Minute)) {
			return connection.AccessToken, nil
		}
		claimed, won, err := s.ClaimRepositoryCredentialRefresh(ctx, connection)
		if err != nil {
			return "", err
		}
		if won {
			return s.RefreshClaimedRepositoryCredential(ctx, provider, claimed)
		}
		if !time.Now().UTC().Before(waitUntil) {
			return "", fmt.Errorf("Repository authorization refresh is in progress; retry the build")
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
		connection, won, err = s.ReadRepositoryConnection(ctx, connection.User, connection.Provider)
		if err != nil {
			return "", err
		}
		if !won {
			return "", fmt.Errorf("Repository connection was removed; reconnect %s", provider.Name)
		}
	}
}

func (s *Service) RefreshClaimedRepositoryCredential(ctx context.Context, provider appconfig.WorkerRepositoryProvider, claimed domain.WorkerRepositoryConnection) (string, error) {
	claimRevision := claimed.TokenRevision
	if claimed.RefreshToken == "" {
		if err := s.SettleRepositoryCredentialRefresh(claimed, claimRevision, true); err != nil {
			return "", err
		}
		return "", reconnectRequired()
	}
	if provider.ClientID == "" {
		_ = s.SettleRepositoryCredentialRefresh(claimed, claimRevision, true)
		return "", fmt.Errorf("Repository OAuth is disabled; reconnect %s after it is configured", provider.Name)
	}
	refreshed, err := (&repooauth.Client{Config: s.Config}).Refresh(ctx, provider, repooauth.Grant{AccessToken: claimed.AccessToken, TokenType: claimed.TokenType, RefreshToken: claimed.RefreshToken})
	if err != nil {
		permanent := RepositoryRefreshGrantRejected(err)
		if versionErr := s.SettleRepositoryCredentialRefresh(claimed, claimRevision, permanent); versionErr != nil {
			return "", versionErr
		}
		if permanent {
			return "", reconnectRequired()
		}
		return "", fmt.Errorf("Repository authorization refresh is temporarily unavailable; retry the build")
	}
	if refreshed.AccessToken == "" {
		_ = s.SettleRepositoryCredentialRefresh(claimed, claimRevision, true)
		return "", reconnectRequired()
	}
	updated := claimed
	updated.AccessToken = refreshed.AccessToken
	updated.TokenType = refreshed.TokenType
	if updated.TokenType == "" {
		updated.TokenType = claimed.TokenType
	}
	updated.RefreshToken = refreshed.RefreshToken
	if updated.RefreshToken == "" {
		updated.RefreshToken = claimed.RefreshToken
	}
	updated.ExpiresAt = refreshed.Expiry
	completed, err := s.CommitRepositoryCredentialRefresh(updated, claimRevision)
	if err != nil {
		return "", err
	}
	if !completed {
		return "", fmt.Errorf("Repository authorization changed concurrently; retry the build")
	}
	return updated.AccessToken, nil
}

func RepositoryRefreshGrantRejected(err error) bool { return repooauth.GrantRejected(err) }

// resolveRepositoryCommit resolves a ref to a full commit id using git's own
// protocol. Tags are peeled to the commit they point at.
func (s *Service) ResolveRepositoryCommit(ctx context.Context, provider appconfig.WorkerRepositoryProvider, source domain.WorkerRepositorySource, token string) (string, error) {
	return (&gitadapter.Client{Config: s.Config}).ResolveRepositoryCommit(ctx, provider, source, token)
}

// FetchRepositorySource reads the complete build project from a pinned commit.
func (s *Service) FetchRepositorySource(ctx context.Context, provider appconfig.WorkerRepositoryProvider, source domain.WorkerBuildRepository, token string) (map[string]string, error) {
	return (&gitadapter.Client{Config: s.Config}).FetchRepositorySource(ctx, provider, source, token)
}

var RepositoryCommit = gitadapter.RepositoryCommit

var RepositoryCloneURL = gitadapter.RepositoryCloneURL

var RepositoryAuth = gitadapter.RepositoryAuth

var PeelToCommit = gitadapter.PeelToCommit

var CloneBaseDir = gitadapter.CloneBaseDir

var CloneRepository = gitadapter.CloneRepository

var MonitorRepositoryClone = gitadapter.MonitorRepositoryClone

var DirectoryWithinLimit = gitadapter.DirectoryWithinLimit

var IsAuthFailure = gitadapter.IsAuthFailure

var SanitizeGitError = gitadapter.SanitizeGitError

var ArchiveRepositoryCommit = gitadapter.ArchiveRepositoryCommit

type RepositoryArchiveLimits = gitadapter.RepositoryArchiveLimits

var ArchiveRepositoryCommitWithLimits = gitadapter.ArchiveRepositoryCommitWithLimits

var ErrRepositoryLimit = gitadapter.ErrRepositoryLimit

type RepositoryLimitedBuffer = gitadapter.RepositoryLimitedBuffer

var RepositoryArchiveError = gitadapter.RepositoryArchiveError
