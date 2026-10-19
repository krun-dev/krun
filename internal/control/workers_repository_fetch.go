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

func (s *Server) repositoryRequest(ctx context.Context, provider WorkerRepositoryProvider, target string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "krun-workers")
	origin, _ := url.Parse(repositoryAPIOrigin(provider))
	if provider.Token != "" && req.URL.Scheme == origin.Scheme && req.URL.Host == origin.Host {
		if provider.Type == "github" {
			req.Header.Set("Authorization", "Bearer "+provider.Token)
		} else {
			req.Header.Set("PRIVATE-TOKEN", provider.Token)
		}
	}
	if provider.Type == "github" {
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	}
	return s.workerRepositoryClient.Do(req)
}

func (s *Server) resolveRepositoryCommit(ctx context.Context, provider WorkerRepositoryProvider, source WorkerRepositorySource) (string, error) {
	var target string
	if provider.Type == "github" {
		target = repositoryAPIOrigin(provider) + "/repos/" + source.Repository + "/commits/" + url.PathEscape(source.Ref)
	} else {
		target = provider.BaseURL + "/api/v4/projects/" + url.PathEscape(source.Repository) + "/repository/commits/" + url.PathEscape(source.Ref)
	}
	resp, err := s.repositoryRequest(ctx, provider, target)
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

func (s *Server) fetchRepositorySource(ctx context.Context, provider WorkerRepositoryProvider, source WorkerBuildRepository) (map[string]string, error) {
	var target string
	if provider.Type == "github" {
		target = repositoryAPIOrigin(provider) + "/repos/" + source.Repository + "/tarball/" + source.Commit
	} else {
		target = provider.BaseURL + "/api/v4/projects/" + url.PathEscape(source.Repository) + "/repository/archive.tar.gz?sha=" + source.Commit
	}
	origin, _ := url.Parse(repositoryAPIOrigin(provider))
	for redirects := 0; redirects < 4; redirects++ {
		resp, err := s.repositoryRequest(ctx, provider, target)
		if err != nil {
			return nil, fmt.Errorf("Repository archive connection unavailable")
		}
		if resp.StatusCode == 301 || resp.StatusCode == 302 || resp.StatusCode == 303 || resp.StatusCode == 307 || resp.StatusCode == 308 {
			next, err := resp.Location()
			resp.Body.Close()
			if err != nil || next.User != nil || next.Fragment != "" {
				return nil, fmt.Errorf("Repository archive redirect is invalid")
			}
			sameOrigin := next.Scheme == origin.Scheme && next.Host == origin.Host
			githubDownload := provider.Type == "github" && next.Scheme == "https" && next.Host == "codeload.github.com"
			if !sameOrigin && !githubDownload {
				return nil, fmt.Errorf("Repository archive redirect is outside the configured provider")
			}
			target = next.String()
			continue
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("Repository archive unavailable (HTTP %d)", resp.StatusCode)
		}
		raw, err := io.ReadAll(io.LimitReader(resp.Body, repositoryArchiveLimit+1))
		resp.Body.Close()
		if err != nil || len(raw) > repositoryArchiveLimit {
			return nil, fmt.Errorf("Repository archive interrupted or exceeds 16 MiB")
		}
		return extractRepositorySource(raw, source)
	}
	return nil, fmt.Errorf("Repository archive has too many redirects")
}

func extractRepositorySource(raw []byte, source WorkerBuildRepository) (map[string]string, error) {
	compressed, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("Repository archive is not a valid tar.gz")
	}
	defer compressed.Close()
	limited := &io.LimitedReader{R: compressed, N: repositoryExpandedLimit + 1}
	archive := tar.NewReader(limited)
	files := make(map[string]string)
	prefix := ""
	total := 0
	for entries := 0; ; entries++ {
		header, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil || entries >= 10000 || limited.N <= 0 {
			return nil, fmt.Errorf("Repository archive is invalid or exceeds extraction limits")
		}
		// GitHub puts commit metadata in a global PAX header before the repository root.
		if header.Typeflag == tar.TypeXGlobalHeader || header.Typeflag == tar.TypeXHeader {
			continue
		}
		name := strings.TrimSuffix(header.Name, "/")
		if name == "" || path.Clean(name) != name || strings.HasPrefix(name, "/") || strings.HasPrefix(name, "../") {
			return nil, fmt.Errorf("Repository archive contains unsafe paths")
		}
		parts := strings.SplitN(name, "/", 2)
		if prefix == "" {
			prefix = parts[0]
		}
		if parts[0] != prefix {
			return nil, fmt.Errorf("Repository archive has multiple roots")
		}
		if len(parts) != 2 || header.Typeflag == tar.TypeDir {
			continue
		}
		name = parts[1]
		if source.RootDirectory != "." {
			if !strings.HasPrefix(name, source.RootDirectory+"/") {
				continue
			}
			name = strings.TrimPrefix(name, source.RootDirectory+"/")
		}
		if header.Typeflag == tar.TypeSymlink || header.Typeflag == tar.TypeLink {
			return nil, fmt.Errorf("Repository project directory cannot contain symbolic links or submodules")
		}
		if header.Typeflag != tar.TypeReg || !contains([]string{".js", ".mjs", ".ts", ".json"}, path.Ext(name)) {
			continue
		}
		if !safeWorkerRepositoryPath(name, false) || header.Size < 0 || header.Size > int64((256<<10)-total) || len(files) >= 32 {
			return nil, fmt.Errorf("Repository source must contain at most 32 safe files and 256 KiB; select a smaller project directory")
		}
		if _, duplicate := files[name]; duplicate {
			return nil, fmt.Errorf("Repository archive contains duplicate source paths")
		}
		data, err := io.ReadAll(archive)
		if err != nil || !utf8.Valid(data) {
			return nil, fmt.Errorf("Repository source is not valid UTF-8")
		}
		total += len(data)
		files[name] = string(data)
	}
	if _, err := io.Copy(io.Discard, limited); err != nil || limited.N <= 0 {
		return nil, fmt.Errorf("Repository archive is invalid or exceeds 64 MiB expanded")
	}
	if err := sourceValid(source.Entrypoint, files); err != nil {
		return nil, fmt.Errorf("Repository project must include the configured JS or TS entrypoint and valid source files")
	}
	return files, nil
}
