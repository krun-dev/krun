package gitrepository

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
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
	workerartifact "github.com/krun-dev/krun/internal/artifact"
	appconfig "github.com/krun-dev/krun/internal/config"
	domain "github.com/krun-dev/krun/internal/domain"
)

type Client struct{ Config *appconfig.Config }

var RepositoryCommit = regexp.MustCompile(`^[a-f0-9]{40}([a-f0-9]{24})?$`)

// repositoryCloneURL is the HTTPS URL git talks to.
func RepositoryCloneURL(provider appconfig.WorkerRepositoryProvider, repository string) string {
	if provider.Type == "github" {
		return provider.BaseURL + "/" + repository + ".git"
	}

	return provider.BaseURL + "/" + repository + ".git"
}

// repositoryAuth builds git credentials from a token. An empty token means an
// anonymous clone, which is the normal path for public repositories.
func RepositoryAuth(provider appconfig.WorkerRepositoryProvider, token string) transport.AuthMethod {
	if token == "" {
		return nil
	}
	if provider.Type == "gitlab" {

		return &httptransport.BasicAuth{Username: "oauth2", Password: token}
	}
	return &httptransport.BasicAuth{Username: "x-access-token", Password: token}
}

// resolveRepositoryCommit resolves a ref to a full commit id using git's own
// protocol. Tags are peeled to the commit they point at.
func (s *Client) ResolveRepositoryCommit(ctx context.Context, provider appconfig.WorkerRepositoryProvider, source domain.WorkerRepositorySource, token string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	remote := git.NewRemote(memory.NewStorage(), &config.RemoteConfig{
		Name: "origin",
		URLs: []string{RepositoryCloneURL(provider, source.Repository)},
	})
	refs, err := remote.ListContext(ctx, &git.ListOptions{Auth: RepositoryAuth(provider, token)})
	if err != nil {
		if IsAuthFailure(err) {
			return "", fmt.Errorf("Repository access denied; connect the provider or check the administrator token")
		}
		return "", fmt.Errorf("Repository or ref unavailable: %v", SanitizeGitError(err))
	}
	ref := source.Ref
	if ref == "" || ref == "HEAD" {
		ref = "HEAD"
	}
	want := []string{"refs/heads/" + ref, "refs/tags/" + ref + "^{}", "refs/tags/" + ref}
	if ref == "HEAD" {
		want = []string{"HEAD"}

		for _, r := range refs {
			if r.Name() == plumbing.HEAD && r.Type() == plumbing.SymbolicReference {
				want = []string{r.Target().String()}
				break
			}
		}
	}
	if RepositoryCommit.MatchString(strings.ToLower(ref)) {

		return strings.ToLower(ref), nil
	}
	for _, candidate := range want {
		for _, r := range refs {
			if r.Name().String() != candidate {
				continue
			}
			hash := r.Hash().String()

			if strings.HasSuffix(candidate, "^{}") || !strings.Contains(candidate, "tags/") {
				return hash, nil
			}
			peeled, e := PeelToCommit(ctx, provider, source.Repository, token, hash, candidate, CloneBaseDir(s.Config.Workers))
			if e != nil {
				return "", e
			}
			return peeled, nil
		}
	}
	return "", fmt.Errorf("Ref %s not found in repository", ref)
}

func PeelToCommit(ctx context.Context, provider appconfig.WorkerRepositoryProvider, repository, token, hash, refName, base string) (string, error) {
	repo, dir, err := CloneRepository(ctx, provider, repository, token, hash, refName, base)
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
func CloneBaseDir(c *appconfig.WorkersConfig) string {
	if c != nil && c.CloneDirectory != "" {
		return c.CloneDirectory
	}
	return os.TempDir()
}

// cloneRepository fetches the pinned commit at depth one, with a bounded
// shallow-ref fallback for servers that reject exact-SHA wants. This preserves
// build pinning without downloading the repository's full history.
func CloneRepository(ctx context.Context, provider appconfig.WorkerRepositoryProvider, repository, token, commit, ref, base string) (*git.Repository, string, error) {
	commit = strings.ToLower(commit)
	if !RepositoryCommit.MatchString(commit) {
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
	remote, err := repo.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{RepositoryCloneURL(provider, repository)}})
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, "", fmt.Errorf("Unable to prepare repository checkout")
	}
	cloneCtx, cancel := context.WithCancel(ctx)
	overLimit := make(chan struct{}, 1)
	monitorDone := make(chan struct{})
	go MonitorRepositoryClone(cloneCtx, cancel, dir, workerartifact.RepositoryCloneDiskLimit, overLimit, monitorDone)
	err = remote.FetchContext(cloneCtx, &git.FetchOptions{
		Auth:     RepositoryAuth(provider, token),
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

		err = remote.FetchContext(cloneCtx, &git.FetchOptions{
			Auth:     RepositoryAuth(provider, token),
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
		within, err = DirectoryWithinLimit(dir, workerartifact.RepositoryCloneDiskLimit)
		if err == nil && !within {
			_ = os.RemoveAll(dir)
			return nil, "", fmt.Errorf("Repository clone exceeds 128 MiB; select a smaller project")
		}
	}
	if err != nil {
		_ = os.RemoveAll(dir)
		if IsAuthFailure(err) {
			return nil, "", fmt.Errorf("Repository access denied; connect the provider or check the administrator token")
		}
		return nil, "", fmt.Errorf("Repository clone failed: %v", SanitizeGitError(err))
	}
	return repo, dir, nil
}

func MonitorRepositoryClone(ctx context.Context, cancel context.CancelFunc, dir string, limit int64, exceeded chan<- struct{}, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			within, err := DirectoryWithinLimit(dir, limit)
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

func DirectoryWithinLimit(root string, limit int64) (bool, error) {
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
			return ErrRepositoryLimit
		}
		return nil
	})
	if errors.Is(err, ErrRepositoryLimit) {
		return false, nil
	}
	return err == nil, err
}

func IsAuthFailure(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "authentication") || strings.Contains(msg, "authorization") ||
		strings.Contains(msg, "401") || strings.Contains(msg, "403")
}

// sanitizeGitError keeps URLs and credentials out of build logs.
func SanitizeGitError(err error) string {
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

// FetchRepositorySource reads the complete build project from a pinned commit.
func (s *Client) FetchRepositorySource(ctx context.Context, provider appconfig.WorkerRepositoryProvider, source domain.WorkerBuildRepository, token string) (map[string]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	commit := source.Commit
	if commit == "" {
		commit = source.Ref
	}
	repo, dir, err := CloneRepository(ctx, provider, source.Repository, token, commit, source.Ref, CloneBaseDir(s.Config.Workers))
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	raw, err := ArchiveRepositoryCommit(repo, commit, source.RootDirectory)
	if err != nil {
		return nil, err
	}
	if len(raw) > workerartifact.RepositoryArchiveLimit {
		return nil, fmt.Errorf("Repository archive exceeds 16 MiB")
	}

	return workerartifact.ExtractRepositorySource(raw, source.RootDirectory)
}

func ArchiveRepositoryCommit(repo *git.Repository, commit, projectRoot string) ([]byte, error) {
	return ArchiveRepositoryCommitWithLimits(repo, commit, projectRoot, RepositoryArchiveLimits{
		MaxCompressedBytes: workerartifact.RepositoryArchiveLimit,
		MaxExpandedBytes:   workerartifact.RepositoryExpandedLimit,
		MaxFileBytes:       workerartifact.RepositorySingleFileLimit,
		MaxFiles:           workerartifact.RepositoryFileLimit,
	})
}

type RepositoryArchiveLimits struct {
	MaxCompressedBytes int
	MaxExpandedBytes   int64
	MaxFileBytes       int64
	MaxFiles           int
}

func ArchiveRepositoryCommitWithLimits(repo *git.Repository, commit, projectRoot string, limits RepositoryArchiveLimits) ([]byte, error) {
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
	if !appconfig.SafeWorkerRepositoryPath(projectRoot, true) {
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

	buf := &RepositoryLimitedBuffer{limit: limits.MaxCompressedBytes}
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
		if files > limits.MaxFiles || file.Size < 0 || file.Size > limits.MaxFileBytes || total > limits.MaxExpandedBytes {
			return ErrRepositoryLimit
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
		return nil, RepositoryArchiveError(err, limits)
	}
	if err := tw.Close(); err != nil {
		return nil, RepositoryArchiveError(err, limits)
	}
	if err := gz.Close(); err != nil {
		return nil, RepositoryArchiveError(err, limits)
	}
	return buf.Bytes(), nil
}

var ErrRepositoryLimit = errors.New("repository resource limit exceeded")

type RepositoryLimitedBuffer struct {
	bytes.Buffer
	limit int
}

func (w *RepositoryLimitedBuffer) Write(data []byte) (int, error) {
	if len(data) > w.limit-w.Len() {
		return 0, ErrRepositoryLimit
	}
	return w.Buffer.Write(data)
}

func RepositoryArchiveError(err error, limits RepositoryArchiveLimits) error {
	if errors.Is(err, ErrRepositoryLimit) {
		return fmt.Errorf("Repository archive exceeds resource limits (%d MiB compressed, %d MiB expanded, %d files, %d MiB per file)", limits.MaxCompressedBytes>>20, limits.MaxExpandedBytes>>20, limits.MaxFiles, limits.MaxFileBytes>>20)
	}
	return err
}
