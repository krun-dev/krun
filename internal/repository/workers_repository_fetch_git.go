package repository

import (
	"context"
	"fmt"
	"time"

	gitadapter "github.com/krun-dev/krun/internal/adapters/gitrepository"
	repooauth "github.com/krun-dev/krun/internal/adapters/repositoryoauth"
	appconfig "github.com/krun-dev/krun/internal/config"
	domain "github.com/krun-dev/krun/internal/domain"
	storage "github.com/krun-dev/krun/internal/storage"
)

// repositoryCredential picks the token for a clone: the connecting user's OAuth
// grant first, the provider's shared PAT as a fallback.
func (s *Service) RepositoryCredential(ctx context.Context, provider appconfig.WorkerRepositoryProvider, userID string) (string, error) {
	if userID == "" {
		return provider.Token, nil
	}
	connection, ok, err := s.ReadRepositoryConnection(ctx, userID, provider.ID)
	if err != nil {
		return "", err
	}
	if !ok {
		return provider.Token, nil
	}

	if connection.ReconnectRequired || connection.AccessToken == "" {
		return "", fmt.Errorf("Repository connection expired; reconnect %s", provider.Name)
	}
	if connection.ExpiresAt.IsZero() || connection.ExpiresAt.After(time.Now().UTC().Add(time.Minute)) {
		return connection.AccessToken, nil
	}
	return s.RefreshRepositoryCredential(ctx, provider, connection)
}

func (s *Service) ReadRepositoryConnection(ctx context.Context, user, provider string) (domain.WorkerRepositoryConnection, bool, error) {
	if s.Store.Persistent() {
		return s.Store.RepositoryConnection(ctx, user, provider)
	}
	var connection domain.WorkerRepositoryConnection
	var ok bool
	var err error
	err = s.Store.View(ctx, storage.PartitionWorkers, func(*domain.State) error {
		connection, ok, err = s.Store.RepositoryConnection(ctx, user, provider)
		return err
	})
	return connection, ok, err
}

func (s *Service) ClaimRepositoryCredentialRefresh(ctx context.Context, connection domain.WorkerRepositoryConnection) (domain.WorkerRepositoryConnection, bool, error) {
	expected := connection.TokenRevision
	if expected == 0 {
		expected = 1
	}
	if s.Store.Persistent() {
		return s.Store.ClaimRepositoryConnectionRefresh(ctx, connection.User, connection.Provider, expected, time.Now().UTC())
	}
	var claimed domain.WorkerRepositoryConnection
	var ok bool
	var err error
	err = s.Store.Update(ctx, storage.PartitionWorkers, func(*domain.State) error {
		claimed, ok, err = s.Store.ClaimRepositoryConnectionRefresh(ctx, connection.User, connection.Provider, expected, time.Now().UTC())
		return err
	})
	return claimed, ok, err
}

func (s *Service) CompleteRepositoryCredentialRefresh(ctx context.Context, connection domain.WorkerRepositoryConnection, claimRevision int64) (bool, error) {
	if s.Store.Persistent() {
		return s.Store.CompleteRepositoryConnectionRefresh(ctx, connection, claimRevision)
	}
	var completed bool
	var err error
	err = s.Store.Update(ctx, storage.PartitionWorkers, func(*domain.State) error {
		completed, err = s.Store.CompleteRepositoryConnectionRefresh(ctx, connection, claimRevision)
		return err
	})
	return completed, err
}

func (s *Service) FailRepositoryCredentialRefresh(ctx context.Context, connection domain.WorkerRepositoryConnection, claimRevision int64, reconnect bool) error {
	if s.Store.Persistent() {
		_, err := s.Store.FailRepositoryConnectionRefresh(ctx, connection.User, connection.Provider, claimRevision, reconnect)
		return err
	}
	return s.Store.Update(ctx, storage.PartitionWorkers, func(*domain.State) error {
		_, err := s.Store.FailRepositoryConnectionRefresh(ctx, connection.User, connection.Provider, claimRevision, reconnect)
		return err
	})
}

// A provider may rotate the refresh token before the build request context is
// cancelled. Finalize or release the durable lease with a short independent
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
			return "", fmt.Errorf("Repository connection expired; reconnect %s", provider.Name)
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
		return "", fmt.Errorf("Repository connection expired; reconnect %s", provider.Name)
	}
	if provider.ClientID == "" {
		_ = s.SettleRepositoryCredentialRefresh(claimed, claimRevision, true)
		return "", fmt.Errorf("Repository OAuth is disabled; reconnect %s after it is configured", provider.Name)
	}
	refreshed, err := (&repooauth.Client{Config: s.Config}).Refresh(ctx, provider, repooauth.Grant{AccessToken: claimed.AccessToken, TokenType: claimed.TokenType, RefreshToken: claimed.RefreshToken})
	if err != nil {
		permanent := RepositoryRefreshGrantRejected(err)
		if releaseErr := s.SettleRepositoryCredentialRefresh(claimed, claimRevision, permanent); releaseErr != nil {
			return "", releaseErr
		}
		if permanent {
			return "", fmt.Errorf("Repository connection expired; reconnect %s", provider.Name)
		}
		return "", fmt.Errorf("Repository authorization refresh is temporarily unavailable; retry the build")
	}
	if refreshed.AccessToken == "" {
		_ = s.SettleRepositoryCredentialRefresh(claimed, claimRevision, true)
		return "", fmt.Errorf("Repository connection expired; reconnect %s", provider.Name)
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

// fetchRepositorySource clones the pinned commit and returns the files needed
// for the build decision plus a tarball of the tree for asset extraction.
func (s *Service) FetchRepositorySource(ctx context.Context, provider appconfig.WorkerRepositoryProvider, source domain.WorkerBuildRepository, token string) (map[string]string, []byte, error) {
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
