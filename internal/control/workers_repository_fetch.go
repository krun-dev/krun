package control

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const repositoryArchiveLimit = 16 << 20
const repositoryExpandedLimit = 64 << 20

var repositoryCommit = regexp.MustCompile(`^[a-f0-9]{40}([a-f0-9]{24})?$`)

func newWorkerRepositoryClient() *http.Client {
	return &http.Client{
		Timeout:       20 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func repositoryAPIOrigin(provider WorkerRepositoryProvider) string {
	if provider.Type == "github" {
		return "https://api.github.com"
	}
	return provider.BaseURL
}

// repositoryCredential picks the token for a fetch: the connecting user's
// OAuth grant first, the provider's shared PAT as a fallback.
func (s *Server) repositoryCredential(st *State, provider WorkerRepositoryProvider, userID string) string {
	if st != nil && userID != "" {
		if c, ok := st.RepositoryConnections[userID+"\x00"+provider.ID]; ok && c.AccessToken != "" {
			return c.AccessToken
		}
	}
	return provider.Token
}

func (s *Server) repositoryRequestWithToken(ctx context.Context, provider WorkerRepositoryProvider, target, token string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "krun-workers")
	origin, _ := url.Parse(repositoryAPIOrigin(provider))
	if token != "" && req.URL.Scheme == origin.Scheme && req.URL.Host == origin.Host {
		if provider.Type == "github" {
			req.Header.Set("Authorization", "Bearer "+token)
		} else {
			req.Header.Set("PRIVATE-TOKEN", token)
		}
	}
	if provider.Type == "github" {
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	}
	return s.workerRepositoryClient.Do(req)
}

func (s *Server) resolveRepositoryCommit(ctx context.Context, provider WorkerRepositoryProvider, source WorkerRepositorySource, token string) (string, error) {
	var target string
	if provider.Type == "github" {
		target = repositoryAPIOrigin(provider) + "/repos/" + source.Repository + "/commits/" + url.PathEscape(source.Ref)
	} else {
		target = provider.BaseURL + "/api/v4/projects/" + url.PathEscape(source.Repository) + "/repository/commits/" + url.PathEscape(source.Ref)
	}
	resp, err := s.repositoryRequestWithToken(ctx, provider, target, token)
	if err != nil {
		return "", fmt.Errorf("Repository connection unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Repository or ref unavailable (HTTP %d); check repository access and administrator credentials", resp.StatusCode)
	}
	var result struct {
		SHA string `json:"sha"`
		ID  string `json:"id"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&result); err != nil {
		return "", fmt.Errorf("Repository returned an invalid commit response")
	}
	commit := result.SHA
	if provider.Type == "gitlab" {
		commit = result.ID
	}
	if !repositoryCommit.MatchString(commit) {
		return "", fmt.Errorf("Repository returned an invalid commit ID")
	}
	return commit, nil
}

func (s *Server) fetchRepositorySource(ctx context.Context, provider WorkerRepositoryProvider, source WorkerBuildRepository, token string) (map[string]string, []byte, error) {
	var target string
	if provider.Type == "github" {
		target = repositoryAPIOrigin(provider) + "/repos/" + source.Repository + "/tarball/" + source.Commit
	} else {
		target = provider.BaseURL + "/api/v4/projects/" + url.PathEscape(source.Repository) + "/repository/archive.tar.gz?sha=" + source.Commit
	}
	origin, _ := url.Parse(repositoryAPIOrigin(provider))
	for redirects := 0; redirects < 4; redirects++ {
		resp, err := s.repositoryRequestWithToken(ctx, provider, target, token)
		if err != nil {
			return nil, nil, fmt.Errorf("Repository archive connection unavailable")
		}
		if resp.StatusCode == 301 || resp.StatusCode == 302 || resp.StatusCode == 303 || resp.StatusCode == 307 || resp.StatusCode == 308 {
			next, err := resp.Location()
			resp.Body.Close()
			if err != nil || next.User != nil || next.Fragment != "" {
				return nil, nil, fmt.Errorf("Repository archive redirect is invalid")
			}
			sameOrigin := next.Scheme == origin.Scheme && next.Host == origin.Host
			githubDownload := provider.Type == "github" && next.Scheme == "https" && next.Host == "codeload.github.com"
			if !sameOrigin && !githubDownload {
				return nil, nil, fmt.Errorf("Repository archive redirect is outside the configured provider")
			}
			target = next.String()
			continue
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, nil, fmt.Errorf("Repository archive unavailable (HTTP %d)", resp.StatusCode)
		}
		raw, err := io.ReadAll(io.LimitReader(resp.Body, repositoryArchiveLimit+1))
		resp.Body.Close()
		if err != nil || len(raw) > repositoryArchiveLimit {
			return nil, nil, fmt.Errorf("Repository archive interrupted or exceeds 16 MiB")
		}
		// Source first: the wrangler config it contains decides the asset
		// directory, so assets are extracted by the caller afterwards.
		files, _, err := extractRepositorySource(raw, source.RootDirectory, source.Entrypoint)
		if err != nil {
			return nil, nil, err
		}
		return files, raw, nil
	}
	return nil, nil, fmt.Errorf("Repository archive has too many redirects")
}

// extractRepositorySource walks the archive twice: the first pass only collects
// the files needed to decide the build layout (the entrypoint tree plus any
// wrangler config), the caller resolves the layout, and the second pass applies
// it. That ordering is what lets a wrangler config decide its own asset
// directory without a chicken-and-egg dependency.
func extractRepositorySource(raw []byte, rootDirectory, entrypoint string) (map[string]string, map[string][]byte, error) {
	files, assets, err := extractArchive(raw, rootDirectory, nil, true)
	if err != nil {
		return nil, nil, err
	}
	// When the entrypoint is already known, it must be present in the tree.
	// Otherwise wrangler config supplies it and the caller validates after.
	if entrypoint != "" {
		if err := sourceEntryValid(entrypoint, files); err != nil {
			return nil, nil, fmt.Errorf("Repository project must include the configured JS or TS entrypoint")
		}
	}
	return files, assets, nil
}

// extractRepositoryAssets re-reads the archive for the resolved asset directory.
func extractRepositoryAssets(raw []byte, rootDirectory, directory string) (map[string][]byte, error) {
	_, assets, err := extractArchive(raw, rootDirectory, &WorkerAssetsConfig{Directory: directory}, false)
	return assets, err
}

func extractArchive(raw []byte, rootDirectory string, assets *WorkerAssetsConfig, collectSource bool) (map[string]string, map[string][]byte, error) {
	compressed, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, nil, fmt.Errorf("Repository archive is not a valid tar.gz")
	}
	defer compressed.Close()
	limited := &io.LimitedReader{R: compressed, N: repositoryExpandedLimit + 1}
	archive := tar.NewReader(limited)
	files := make(map[string]string)
	assetFiles := make(map[string][]byte)
	prefix := ""
	total := 0
	assetTotal := 0
	for entries := 0; ; entries++ {
		header, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil || entries >= 10000 || limited.N <= 0 {
			return nil, nil, fmt.Errorf("Repository archive is invalid or exceeds extraction limits")
		}
		// GitHub puts commit metadata in a global PAX header before the repository root.
		if header.Typeflag == tar.TypeXGlobalHeader || header.Typeflag == tar.TypeXHeader {
			continue
		}
		name := strings.TrimSuffix(header.Name, "/")
		if name == "" || path.Clean(name) != name || strings.HasPrefix(name, "/") || strings.HasPrefix(name, "../") {
			return nil, nil, fmt.Errorf("Repository archive contains unsafe paths")
		}
		parts := strings.SplitN(name, "/", 2)
		if prefix == "" {
			prefix = parts[0]
		}
		if parts[0] != prefix {
			return nil, nil, fmt.Errorf("Repository archive has multiple roots")
		}
		if len(parts) != 2 || header.Typeflag == tar.TypeDir {
			continue
		}
		name = parts[1]
		if rootDirectory != "" && rootDirectory != "." {
			if !strings.HasPrefix(name, rootDirectory+"/") {
				continue
			}
			name = strings.TrimPrefix(name, rootDirectory+"/")
		}
		if header.Typeflag == tar.TypeSymlink || header.Typeflag == tar.TypeLink {
			return nil, nil, fmt.Errorf("Repository project directory cannot contain symbolic links or submodules")
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		if assets != nil && strings.HasPrefix(name, assets.Directory+"/") {
			asset := strings.TrimPrefix(name, assets.Directory+"/")
			if _, ok := workerAssetPath(asset); !ok || header.Size < 0 || header.Size > workerAssetMax || len(assetFiles) >= workerAssetsFilesMax {
				return nil, nil, fmt.Errorf("Repository assets must contain at most %d files of %d MiB", workerAssetsFilesMax, workerAssetMax>>20)
			}
			if _, ok := workerAssetType(asset); !ok {
				return nil, nil, fmt.Errorf("repository asset type is not supported: %s", asset)
			}
			data, err := io.ReadAll(archive)
			if err != nil || len(data) != int(header.Size) {
				return nil, nil, fmt.Errorf("repository asset is invalid: %s", asset)
			}
			assetTotal += len(data)
			if assetTotal > workerAssetsTotalMax {
				return nil, nil, fmt.Errorf("Repository assets exceed 20 MiB")
			}
			assetFiles[asset] = data
			continue
		}
		// wrangler config declares the entrypoint and asset directory, so it must
		// be retained even though it is not compilable source.
		isWranglerConfig := false
		for _, cfgName := range wranglerFileNames {
			if name == cfgName {
				isWranglerConfig = true
				break
			}
		}
		if !isWranglerConfig && !collectSource {
			continue
		}
		if !isWranglerConfig {
			// Type declarations are tooling input, not compilable source; standard
			// workers projects ship one that can be hundreds of KiB.
			if strings.HasSuffix(name, ".d.ts") {
				continue
			}
			// Unrelated repository files must not fail the build: a project can sit
			// next to lockfiles, images and fixtures.
			if !contains([]string{".js", ".mjs", ".ts", ".json"}, path.Ext(name)) {
				continue
			}
			if header.Size > int64(256<<10) {
				return nil, nil, fmt.Errorf("Repository source file %s exceeds 256 KiB", name)
			}
		}
		if !safeWorkerRepositoryPath(name, false) || header.Size < 0 || header.Size > int64((256<<10)-total) || len(files) >= 32 {
			return nil, nil, fmt.Errorf("Repository source must contain at most 32 safe files and 256 KiB; select a smaller project directory")
		}
		if _, duplicate := files[name]; duplicate {
			return nil, nil, fmt.Errorf("Repository archive contains duplicate source paths")
		}
		data, err := io.ReadAll(archive)
		if err != nil || !utf8.Valid(data) {
			return nil, nil, fmt.Errorf("Repository source is not valid UTF-8")
		}
		total += len(data)
		files[name] = string(data)
	}
	if _, err := io.Copy(io.Discard, limited); err != nil || limited.N <= 0 {
		return nil, nil, fmt.Errorf("Repository archive is invalid or exceeds 64 MiB expanded")
	}
	// Entrypoint and asset-completeness checks happen after the layout is
	// resolved: both may come from wrangler config, which is only readable once
	// the tree is in hand.
	return files, assetFiles, nil
}