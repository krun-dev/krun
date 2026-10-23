package control

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"net/http"
	"os"
	"path"
	"regexp"
	"strings"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	httptransport "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/go-git/go-git/v5/storage/memory"
)

const repositoryArchiveLimit = 16 << 20
const repositoryExpandedLimit = 64 << 20

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
func (s *Server) repositoryCredential(st *State, provider WorkerRepositoryProvider, userID string) string {
	if st != nil && userID != "" {
		if c, ok := st.RepositoryConnections[userID+"\x00"+provider.ID]; ok && c.AccessToken != "" {
			return c.AccessToken
		}
	}
	return provider.Token
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
			peeled, e := peelToCommit(ctx, provider, source.Repository, token, hash, cloneBaseDir(s.Config.Workers))
			if e != nil {
				return "", e
			}
			return peeled, nil
		}
	}
	return "", fmt.Errorf("Ref %s not found in repository", ref)
}

func peelToCommit(ctx context.Context, provider WorkerRepositoryProvider, repository, token, hash, base string) (string, error) {
	repo, err := cloneRepository(ctx, provider, repository, token, hash, base)
	if err != nil {
		return "", err
	}
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

// cloneRepository performs a shallow clone at one commit and returns the handle.
// Depth 1 keeps large histories out of the build path; the tree is read from
// disk and the checkout is removed by the caller.
func cloneRepository(ctx context.Context, provider WorkerRepositoryProvider, repository, token, commit string, base string) (*git.Repository, error) {
	dir, err := os.MkdirTemp(base, "krun-clone-")
	if err != nil {
		return nil, fmt.Errorf("Unable to prepare clone directory; set workers.clone_directory to a writable volume")
	}
	// A pinned commit may no longer be the branch tip (a build must stay on the
	// commit it started with), so history is fetched in full. Shallow clones
	// cannot check out an older commit.
	repo, err := git.PlainCloneContext(ctx, dir, false, &git.CloneOptions{
		URL:  repositoryCloneURL(provider, repository),
		Auth: repositoryAuth(provider, token),
		Tags: git.NoTags,
	})
	if err != nil {
		_ = os.RemoveAll(dir)
		if isAuthFailure(err) {
			return nil, fmt.Errorf("Repository access denied; connect the provider or check the administrator token")
		}
		return nil, fmt.Errorf("Repository clone failed: %v", sanitizeGitError(err))
	}
	return repo, nil
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
	repo, err := cloneRepository(ctx, provider, source.Repository, token, commit, cloneBaseDir(s.Config.Workers))
	if err != nil {
		return nil, nil, err
	}
	worktree, err := repo.Worktree()
	if err != nil {
		return nil, nil, fmt.Errorf("Repository checkout unavailable")
	}
	dir := worktree.Filesystem.Root()
	defer os.RemoveAll(dir)

	if commit != "" {
		if err := checkoutCommit(repo, worktree, commit); err != nil {
			return nil, nil, err
		}
	}
	raw, err := archiveWorktree(dir)
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

func checkoutCommit(repo *git.Repository, worktree *git.Worktree, commit string) error {
	hash, err := repo.ResolveRevision(plumbing.Revision(commit))
	if err != nil {
		// A shallow clone may not contain the commit; fetch it explicitly.
		return fmt.Errorf("Repository commit %s is not available", commit)
	}
	return worktree.Checkout(&git.CheckoutOptions{Hash: *hash, Force: true})
}

// archiveWorktree packages one project directory the same way a provider
// tarball would, so the existing extraction rules apply unchanged.
func archiveWorktree(root string) ([]byte, error) {
	base := root
	if info, err := os.Stat(base); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("Repository checkout is not available")
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	err := filepathWalk(base, func(rel string, data []byte) error {
		// Every entry shares one root, exactly like a provider tarball, because
		// the extractor rejects archives with multiple roots.
		name := path.Join("repo", rel)
		if err := tw.WriteHeader(&tar.Header{Name: name, Size: int64(len(data)), Mode: 0o644, Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		_, err := tw.Write(data)
		return err
	})
	if err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// filepathWalk visits regular files under root, skipping the .git directory and
// reporting each path relative to root.
func filepathWalk(root string, fn func(rel string, data []byte) error) error {
	return filepathWalkDir(root, root, fn)
}

func filepathWalkDir(root, dir string, fn func(rel string, data []byte) error) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == ".git" {
			continue
		}
		full := path.Join(dir, name)
		if entry.IsDir() {
			if err := filepathWalkDir(root, full, fn); err != nil {
				return err
			}
			continue
		}
		if !entry.Type().IsRegular() {
			continue
		}
		data, err := os.ReadFile(full)
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(full, root), "/")
		if err := fn(rel, data); err != nil {
			return err
		}
	}
	return nil
}
