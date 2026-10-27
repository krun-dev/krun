package control

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
	httptransport "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/go-git/go-git/v5/storage/memory"
	"golang.org/x/oauth2"
)

const repositoryArchiveLimit = 16 << 20
const repositoryExpandedLimit = 64 << 20
const repositoryCloneDiskLimit = 128 << 20
const repositoryFileLimit = 10000
const repositorySingleFileLimit = workerAssetMax

var repositoryCommit = regexp.MustCompile(`^[a-f0-9]{40}([a-f0-9]{24})?$`)

// Repository access goes over the git protocol, not the provider's REST API.
// Git has no per-IP call quota and needs no token for public repositories, so a
// build is never blocked by rate limiting; a user's OAuth token is only used to
// authenticate the clone for private repositories.

func newWorkerRepositoryClient() *http.Client {
	return &http.Client{
		Timeout:       20 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// repositoryCredential picks the token for a clone: the connecting user's OAuth
// grant first, the provider's shared PAT as a fallback.
func (s *Server) repositoryCredential(ctx context.Context, provider WorkerRepositoryProvider, userID string) (string, error) {
	if userID == "" {
		return provider.Token, nil
	}
	connection, ok, err := s.readRepositoryConnection(ctx, userID, provider.ID)
	if err != nil {
		return "", err
	}
	if !ok {
		return provider.Token, nil
	}
	// Once a user has connected this provider, never silently fall back to the
	// administrator PAT: that changes both repository visibility and audit
	// identity. An unusable user grant must be reconnected explicitly.
	if connection.ReconnectRequired || connection.AccessToken == "" {
		return "", fmt.Errorf("Repository connection expired; reconnect %s", provider.Name)
	}
	if connection.ExpiresAt.IsZero() || connection.ExpiresAt.After(now().Add(time.Minute)) {
		return connection.AccessToken, nil
	}
	return s.refreshRepositoryCredential(ctx, provider, connection)
}

func (s *Server) readRepositoryConnection(ctx context.Context, user, provider string) (WorkerRepositoryConnection, bool, error) {
	if s.Store.pool != nil {
		return s.Store.repositoryConnection(ctx, user, provider)
	}
	var connection WorkerRepositoryConnection
	var ok bool
	var err error
	err = s.Store.View(ctx, PartitionWorkers, func(*State) error {
		connection, ok, err = s.Store.repositoryConnection(ctx, user, provider)
		return err
	})
	return connection, ok, err
}

func (s *Server) claimRepositoryCredentialRefresh(ctx context.Context, connection WorkerRepositoryConnection) (WorkerRepositoryConnection, bool, error) {
	expected := connection.TokenRevision
	if expected == 0 {
		expected = 1
	}
	if s.Store.pool != nil {
		return s.Store.claimRepositoryConnectionRefresh(ctx, connection.User, connection.Provider, expected, now())
	}
	var claimed WorkerRepositoryConnection
	var ok bool
	var err error
	err = s.Store.Update(ctx, PartitionWorkers, func(*State) error {
		claimed, ok, err = s.Store.claimRepositoryConnectionRefresh(ctx, connection.User, connection.Provider, expected, now())
		return err
	})
	return claimed, ok, err
}

func (s *Server) completeRepositoryCredentialRefresh(ctx context.Context, connection WorkerRepositoryConnection, claimRevision int64) (bool, error) {
	if s.Store.pool != nil {
		return s.Store.completeRepositoryConnectionRefresh(ctx, connection, claimRevision)
	}
	var completed bool
	var err error
	err = s.Store.Update(ctx, PartitionWorkers, func(*State) error {
		completed, err = s.Store.completeRepositoryConnectionRefresh(ctx, connection, claimRevision)
		return err
	})
	return completed, err
}

func (s *Server) failRepositoryCredentialRefresh(ctx context.Context, connection WorkerRepositoryConnection, claimRevision int64, reconnect bool) error {
	if s.Store.pool != nil {
		_, err := s.Store.failRepositoryConnectionRefresh(ctx, connection.User, connection.Provider, claimRevision, reconnect)
		return err
	}
	return s.Store.Update(ctx, PartitionWorkers, func(*State) error {
		_, err := s.Store.failRepositoryConnectionRefresh(ctx, connection.User, connection.Provider, claimRevision, reconnect)
		return err
	})
}

// A provider may rotate the refresh token before the build request context is
// cancelled. Finalize or release the durable lease with a short independent
// context so cancellation cannot strand or lose the newly issued grant.
func (s *Server) settleRepositoryCredentialRefresh(connection WorkerRepositoryConnection, claimRevision int64, reconnect bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return s.failRepositoryCredentialRefresh(ctx, connection, claimRevision, reconnect)
}

func (s *Server) commitRepositoryCredentialRefresh(connection WorkerRepositoryConnection, claimRevision int64) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return s.completeRepositoryCredentialRefresh(ctx, connection, claimRevision)
}

func (s *Server) refreshRepositoryCredential(ctx context.Context, provider WorkerRepositoryProvider, initial WorkerRepositoryConnection) (string, error) {
	connection := initial
	waitUntil := now().Add(10 * time.Second)
	for {
		if connection.ReconnectRequired || connection.AccessToken == "" {
			return "", fmt.Errorf("Repository connection expired; reconnect %s", provider.Name)
		}
		if connection.ExpiresAt.IsZero() || connection.ExpiresAt.After(now().Add(time.Minute)) {
			return connection.AccessToken, nil
		}
		claimed, won, err := s.claimRepositoryCredentialRefresh(ctx, connection)
		if err != nil {
			return "", err
		}
		if won {
			return s.refreshClaimedRepositoryCredential(ctx, provider, claimed)
		}
		if !now().Before(waitUntil) {
			return "", fmt.Errorf("Repository authorization refresh is in progress; retry the build")
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
		connection, won, err = s.readRepositoryConnection(ctx, connection.User, connection.Provider)
		if err != nil {
			return "", err
		}
		if !won {
			return "", fmt.Errorf("Repository connection was removed; reconnect %s", provider.Name)
		}
	}
}

func (s *Server) refreshClaimedRepositoryCredential(ctx context.Context, provider WorkerRepositoryProvider, claimed WorkerRepositoryConnection) (string, error) {
	claimRevision := claimed.TokenRevision
	if claimed.RefreshToken == "" {
		if err := s.settleRepositoryCredentialRefresh(claimed, claimRevision, true); err != nil {
			return "", err
		}
		return "", fmt.Errorf("Repository connection expired; reconnect %s", provider.Name)
	}
	conf := s.repositoryOAuthConfig(provider)
	if conf == nil {
		_ = s.settleRepositoryCredentialRefresh(claimed, claimRevision, true)
		return "", fmt.Errorf("Repository OAuth is disabled; reconnect %s after it is configured", provider.Name)
	}
	// Force oauth2 to exchange the refresh grant. The caller already decided
	// the access token is within the refresh window.
	current := &oauth2.Token{
		AccessToken: claimed.AccessToken, TokenType: claimed.TokenType,
		RefreshToken: claimed.RefreshToken, Expiry: time.Unix(1, 0),
	}
	oauthContext, cancel, contextErr := s.repositoryOAuthContext(ctx, provider)
	if contextErr != nil {
		_ = s.settleRepositoryCredentialRefresh(claimed, claimRevision, false)
		return "", fmt.Errorf("Repository authorization refresh is temporarily unavailable; retry the build")
	}
	defer cancel()
	refreshed, err := conf.TokenSource(oauthContext, current).Token()
	if err != nil {
		permanent := repositoryRefreshGrantRejected(err)
		if releaseErr := s.settleRepositoryCredentialRefresh(claimed, claimRevision, permanent); releaseErr != nil {
			return "", releaseErr
		}
		if permanent {
			return "", fmt.Errorf("Repository connection expired; reconnect %s", provider.Name)
		}
		return "", fmt.Errorf("Repository authorization refresh is temporarily unavailable; retry the build")
	}
	if refreshed.AccessToken == "" {
		_ = s.settleRepositoryCredentialRefresh(claimed, claimRevision, true)
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
	completed, err := s.commitRepositoryCredentialRefresh(updated, claimRevision)
	if err != nil {
		return "", err
	}
	if !completed {
		return "", fmt.Errorf("Repository authorization changed concurrently; retry the build")
	}
	return updated.AccessToken, nil
}

func repositoryRefreshGrantRejected(err error) bool {
	var retrieve *oauth2.RetrieveError
	if !errors.As(err, &retrieve) || retrieve.Response == nil {
		return false
	}
	return retrieve.Response.StatusCode == http.StatusBadRequest || retrieve.Response.StatusCode == http.StatusUnauthorized
}

// repositoryCloneURL is the HTTPS URL git talks to.
func repositoryCloneURL(provider WorkerRepositoryProvider, repository string) string {
	if provider.Type == "github" {
		return provider.BaseURL + "/" + repository + ".git"
	}
	// GitLab and compatible servers expose the same path over https.
	return provider.BaseURL + "/" + repository + ".git"
}

// repositoryAuth builds git credentials from a token. An empty token means an
// anonymous clone, which is the normal path for public repositories.
func repositoryAuth(provider WorkerRepositoryProvider, token string) transport.AuthMethod {
	if token == "" {
		return nil
	}
	if provider.Type == "gitlab" {
		// GitLab accepts an OAuth token as the password with any username.
		return &httptransport.BasicAuth{Username: "oauth2", Password: token}
	}
	return &httptransport.BasicAuth{Username: "x-access-token", Password: token}
}

// resolveRepositoryCommit resolves a ref to a full commit id using git's own
// protocol. Tags are peeled to the commit they point at.
func (s *Server) resolveRepositoryCommit(ctx context.Context, provider WorkerRepositoryProvider, source WorkerRepositorySource, token string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	remote := git.NewRemote(memory.NewStorage(), &config.RemoteConfig{
		Name: "origin",
		URLs: []string{repositoryCloneURL(provider, source.Repository)},
	})
	refs, err := remote.ListContext(ctx, &git.ListOptions{Auth: repositoryAuth(provider, token)})
	if err != nil {
		if isAuthFailure(err) {
			return "", fmt.Errorf("Repository access denied; connect the provider or check the administrator token")
		}
		return "", fmt.Errorf("Repository or ref unavailable: %v", sanitizeGitError(err))
	}
	ref := source.Ref
	if ref == "" || ref == "HEAD" {
		ref = "HEAD"
	}
	want := []string{"refs/heads/" + ref, "refs/tags/" + ref + "^{}", "refs/tags/" + ref}
	if repositoryCommit.MatchString(strings.ToLower(ref)) {
		// A pinned commit needs no ref lookup; the caller already knows it.
		return strings.ToLower(ref), nil
	}
	for _, candidate := range want {
		for _, r := range refs {
			if r.Name().String() != candidate {
				continue
			}
			hash := r.Hash().String()
			// An annotated tag points at a tag object; keep peeling until commit.
			if strings.HasSuffix(candidate, "^{}") || !strings.Contains(candidate, "tags/") {
				return hash, nil
			}
			peeled, e := peelToCommit(ctx, provider, source.Repository, token, hash, candidate, cloneBaseDir(s.Config.Workers))
			if e != nil {
				return "", e
			}
			return peeled, nil
		}
	}
	return "", fmt.Errorf("Ref %s not found in repository", ref)
}

func peelToCommit(ctx context.Context, provider WorkerRepositoryProvider, repository, token, hash, refName, base string) (string, error) {
	repo, dir, err := cloneRepository(ctx, provider, repository, token, hash, refName, base)
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	ref, err := repo.ResolveRevision(plumbing.Revision(hash))
	if err != nil {
		return "", fmt.Errorf("Repository returned an invalid commit ID")
	}
	return ref.String(), nil
}

// cloneBaseDir is where clones are materialised. The control plane may run with
// a read-only root filesystem, so the path is configurable and must point at a
// writable volume in that case.
func cloneBaseDir(c *WorkersConfig) string {
	if c != nil && c.CloneDirectory != "" {
		return c.CloneDirectory
	}
	return os.TempDir()
}

// cloneRepository fetches the pinned commit at depth one, with a bounded
// shallow-ref fallback for servers that reject exact-SHA wants. This preserves
// build pinning without downloading the repository's full history.
func cloneRepository(ctx context.Context, provider WorkerRepositoryProvider, repository, token, commit, ref, base string) (*git.Repository, string, error) {
	commit = strings.ToLower(commit)
	if !repositoryCommit.MatchString(commit) {
		return nil, "", fmt.Errorf("Repository returned an invalid commit ID")
	}
	dir, err := os.MkdirTemp(base, "krun-clone-")
	if err != nil {
		return nil, "", fmt.Errorf("Unable to prepare clone directory; set workers.clone_directory to a writable volume")
	}
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, "", fmt.Errorf("Unable to prepare repository checkout")
	}
	remote, err := repo.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{repositoryCloneURL(provider, repository)}})
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, "", fmt.Errorf("Unable to prepare repository checkout")
	}
	cloneCtx, cancel := context.WithCancel(ctx)
	overLimit := make(chan struct{}, 1)
	monitorDone := make(chan struct{})
	go monitorRepositoryClone(cloneCtx, cancel, dir, repositoryCloneDiskLimit, overLimit, monitorDone)
	err = remote.FetchContext(cloneCtx, &git.FetchOptions{
		Auth:     repositoryAuth(provider, token),
		Depth:    1,
		Tags:     git.NoTags,
		Force:    true,
		RefSpecs: []config.RefSpec{config.RefSpec("+" + commit + ":refs/krun/pinned")},
	})
	if errors.Is(err, git.ErrExactSHA1NotSupported) && ref != "" {
		fallback := ref
		if fallback != "HEAD" && !strings.HasPrefix(fallback, "refs/") {
			fallback = "refs/heads/" + fallback
		}
		// A bounded shallow fallback supports servers without exact-SHA wants
		// while still refusing to pull an unbounded history. The requested commit
		// is verified by checkout; a branch that moved beyond this window fails
		// instead of silently building the new tip.
		err = remote.FetchContext(cloneCtx, &git.FetchOptions{
			Auth:     repositoryAuth(provider, token),
			Depth:    32,
			Tags:     git.NoTags,
			Force:    true,
			RefSpecs: []config.RefSpec{config.RefSpec("+" + fallback + ":refs/krun/fallback")},
		})
		if errors.Is(err, git.NoErrAlreadyUpToDate) {
			err = nil
		}
	}
	cancel()
	<-monitorDone
	select {
	case <-overLimit:
		_ = os.RemoveAll(dir)
		return nil, "", fmt.Errorf("Repository clone exceeds 128 MiB; select a smaller project")
	default:
	}
	if err == nil {
		var within bool
		within, err = directoryWithinLimit(dir, repositoryCloneDiskLimit)
		if err == nil && !within {
			_ = os.RemoveAll(dir)
			return nil, "", fmt.Errorf("Repository clone exceeds 128 MiB; select a smaller project")
		}
	}
	if err != nil {
		_ = os.RemoveAll(dir)
		if isAuthFailure(err) {
			return nil, "", fmt.Errorf("Repository access denied; connect the provider or check the administrator token")
		}
		return nil, "", fmt.Errorf("Repository clone failed: %v", sanitizeGitError(err))
	}
	return repo, dir, nil
}

func monitorRepositoryClone(ctx context.Context, cancel context.CancelFunc, dir string, limit int64, exceeded chan<- struct{}, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			within, err := directoryWithinLimit(dir, limit)
			if err == nil && !within {
				select {
				case exceeded <- struct{}{}:
				default:
				}
				cancel()
				return
			}
		}
	}
}

func directoryWithinLimit(root string, limit int64) (bool, error) {
	var total int64
	err := filepath.WalkDir(root, func(_ string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		if total > limit {
			return errRepositoryLimit
		}
		return nil
	})
	if errors.Is(err, errRepositoryLimit) {
		return false, nil
	}
	return err == nil, err
}

func isAuthFailure(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "authentication") || strings.Contains(msg, "authorization") ||
		strings.Contains(msg, "401") || strings.Contains(msg, "403")
}

// sanitizeGitError keeps URLs and credentials out of build logs.
func sanitizeGitError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if idx := strings.Index(msg, "https://"); idx >= 0 {
		msg = msg[:idx] + "repository"
	}
	if len(msg) > 300 {
		msg = msg[:300]
	}
	return msg
}

// fetchRepositorySource clones the pinned commit and returns the files needed
// for the build decision plus a tarball of the tree for asset extraction.
func (s *Server) fetchRepositorySource(ctx context.Context, provider WorkerRepositoryProvider, source WorkerBuildRepository, token string) (map[string]string, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	commit := source.Commit
	if commit == "" {
		commit = source.Ref
	}
	repo, dir, err := cloneRepository(ctx, provider, source.Repository, token, commit, source.Ref, cloneBaseDir(s.Config.Workers))
	if err != nil {
		return nil, nil, err
	}
	defer os.RemoveAll(dir)
	raw, err := archiveRepositoryCommit(repo, commit, source.RootDirectory)
	if err != nil {
		return nil, nil, err
	}
	if len(raw) > repositoryArchiveLimit {
		return nil, nil, fmt.Errorf("Repository archive exceeds 16 MiB")
	}
	files, _, err := extractRepositorySource(raw, source.RootDirectory, source.Entrypoint)
	if err != nil {
		return nil, nil, err
	}
	return files, raw, nil
}

func archiveRepositoryCommit(repo *git.Repository, commit, projectRoot string) ([]byte, error) {
	return archiveRepositoryCommitWithLimits(repo, commit, projectRoot, repositoryArchiveLimits{
		maxCompressedBytes: repositoryArchiveLimit,
		maxExpandedBytes:   repositoryExpandedLimit,
		maxFileBytes:       repositorySingleFileLimit,
		maxFiles:           repositoryFileLimit,
	})
}

type repositoryArchiveLimits struct {
	maxCompressedBytes int
	maxExpandedBytes   int64
	maxFileBytes       int64
	maxFiles           int
}

func archiveRepositoryCommitWithLimits(repo *git.Repository, commit, projectRoot string, limits repositoryArchiveLimits) ([]byte, error) {
	hash, err := repo.ResolveRevision(plumbing.Revision(commit))
	if err != nil {
		return nil, fmt.Errorf("Repository commit %s is not available", commit)
	}
	commitObject, err := repo.CommitObject(*hash)
	if err != nil {
		return nil, fmt.Errorf("Repository commit %s is not available", commit)
	}
	tree, err := commitObject.Tree()
	if err != nil {
		return nil, fmt.Errorf("Repository commit tree is not available")
	}
	if projectRoot == "" {
		projectRoot = "."
	}
	if !safeWorkerRepositoryPath(projectRoot, true) {
		return nil, fmt.Errorf("Repository project directory is invalid")
	}
	archivePrefix := ""
	if projectRoot != "." {
		tree, err = tree.Tree(projectRoot)
		if err != nil {
			return nil, fmt.Errorf("Repository project directory is not available")
		}
		archivePrefix = projectRoot
	}

	buf := &repositoryLimitedBuffer{limit: limits.maxCompressedBytes}
	gz := gzip.NewWriter(buf)
	tw := tar.NewWriter(gz)
	files := 0
	var total int64
	iter := tree.Files()
	err = iter.ForEach(func(file *object.File) error {
		if !file.Mode.IsRegular() && file.Mode != filemode.Executable {
			return fmt.Errorf("Repository project directory cannot contain symbolic links or submodules")
		}
		name := filepath.ToSlash(file.Name)
		if name == "" || path.Clean(name) != name || strings.HasPrefix(name, "/") || strings.HasPrefix(name, "../") {
			return fmt.Errorf("Repository project contains an unsafe path")
		}
		files++
		total += file.Size
		if files > limits.maxFiles || file.Size < 0 || file.Size > limits.maxFileBytes || total > limits.maxExpandedBytes {
			return errRepositoryLimit
		}
		if err := tw.WriteHeader(&tar.Header{Name: path.Join("repo", archivePrefix, name), Size: file.Size, Mode: 0o644, Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		reader, err := file.Reader()
		if err != nil {
			return err
		}
		written, copyErr := io.CopyN(tw, reader, file.Size)
		closeErr := reader.Close()
		if copyErr != nil {
			return copyErr
		}
		if written != file.Size {
			return fmt.Errorf("Repository file changed while preparing the build")
		}
		return closeErr
	})
	iter.Close()
	if err != nil {
		return nil, repositoryArchiveError(err, limits)
	}
	if err := tw.Close(); err != nil {
		return nil, repositoryArchiveError(err, limits)
	}
	if err := gz.Close(); err != nil {
		return nil, repositoryArchiveError(err, limits)
	}
	return buf.Bytes(), nil
}

var errRepositoryLimit = errors.New("repository resource limit exceeded")

type repositoryLimitedBuffer struct {
	bytes.Buffer
	limit int
}

func (w *repositoryLimitedBuffer) Write(data []byte) (int, error) {
	if len(data) > w.limit-w.Len() {
		return 0, errRepositoryLimit
	}
	return w.Buffer.Write(data)
}

func repositoryArchiveError(err error, limits repositoryArchiveLimits) error {
	if errors.Is(err, errRepositoryLimit) {
		return fmt.Errorf("Repository archive exceeds resource limits (%d MiB compressed, %d MiB expanded, %d files, %d MiB per file)", limits.maxCompressedBytes>>20, limits.maxExpandedBytes>>20, limits.maxFiles, limits.maxFileBytes>>20)
	}
	return err
}
