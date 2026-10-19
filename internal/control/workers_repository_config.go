package control

import (
	"fmt"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
)

// Repository credentials belong to the control plane, not builder Jobs or Workers.
type WorkerRepositoryProvider struct {
	ID                  string   `json:"id"`
	Type                string   `json:"type"`
	Name                string   `json:"name"`
	Enabled             bool     `json:"enabled"`
	Priority            int      `json:"priority"`
	BaseURL             string   `json:"base_url"`
	Token               string   `json:"token"`
	AllowedRepositories []string `json:"allowed_repositories"`
	OrganizationIDs     []string `json:"organization_ids"`
}

type WorkerRepositorySource struct {
	ProviderID    string `json:"provider_id"`
	Repository    string `json:"repository"`
	Ref           string `json:"ref"`
	RootDirectory string `json:"root_directory"`
	Entrypoint    string `json:"entrypoint"`
}

type WorkerBuildRepository struct {
	WorkerRepositorySource
	Commit string `json:"commit,omitempty"`
}

type WorkerRepositoryProviderInfo struct {
	ID                   string `json:"id"`
	Type                 string `json:"type"`
	Name                 string `json:"name"`
	Priority             int    `json:"priority"`
	BaseURL              string `json:"base_url"`
	CredentialConfigured bool   `json:"credential_configured"`
}

var repositorySegment = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_.-]*$`)
var repositoryRef = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_./-]{0,199}$`)

func (c *WorkersConfig) validateRepositoryProviders() error {
	seen := make(map[string]bool)
	for i := range c.RepositoryProviders {
		p := &c.RepositoryProviders[i]
		if !slug.MatchString(p.ID) || seen[p.ID] || !contains([]string{"github", "gitlab"}, p.Type) || p.Priority < 0 {
			return fmt.Errorf("Workers repository providers require unique IDs, github/gitlab type and nonnegative priority")
		}
		seen[p.ID] = true
		if p.Name == "" {
			p.Name = p.ID
		}
		if p.BaseURL == "" {
			if p.Type == "github" {
				p.BaseURL = "https://github.com"
			} else {
				p.BaseURL = "https://gitlab.com"
			}
		}
		p.BaseURL = strings.TrimSuffix(p.BaseURL, "/")
		u, err := url.Parse(p.BaseURL)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("Workers repository provider %s requires a fixed HTTPS origin", p.ID)
		}
		if p.Type == "github" && p.BaseURL != "https://github.com" {
			return fmt.Errorf("Workers github provider must use https://github.com")
		}
		if strings.ContainsAny(p.Token, "\r\n") {
			return fmt.Errorf("invalid Workers repository token")
		}
		// A shared private token must have an explicit repository boundary.
		if p.Enabled && p.Token != "" && len(p.AllowedRepositories) == 0 {
			return fmt.Errorf("Workers repository provider %s with a token requires allowed_repositories", p.ID)
		}
		for _, repo := range p.AllowedRepositories {
			if !workerRepositoryNameValid(repo, p.Type) {
				return fmt.Errorf("invalid Workers allowed repository for %s", p.ID)
			}
		}
	}
	return nil
}

func workerRepositoryNameValid(name, kind string) bool {
	parts := strings.Split(name, "/")
	if len(parts) < 2 || len(parts) > 10 || len(name) > 300 || (kind == "github" && len(parts) != 2) {
		return false
	}
	for _, part := range parts {
		if !repositorySegment.MatchString(part) || part == "." || part == ".." || strings.HasSuffix(part, ".git") {
			return false
		}
	}
	return true
}

func (s *Server) repositoryProvider(id, organization string) (WorkerRepositoryProvider, error) {
	for _, p := range s.Config.Workers.RepositoryProviders {
		if p.ID == id && p.Enabled && (len(p.OrganizationIDs) == 0 || contains(p.OrganizationIDs, organization)) {
			return p, nil
		}
	}
	return WorkerRepositoryProvider{}, problem(422, "WORKER_REPOSITORY_PROVIDER_DISABLED", "Repository provider is not enabled for this organization")
}

func (s *Server) validateRepositorySource(source *WorkerRepositorySource, organization string) (WorkerRepositoryProvider, error) {
	p, err := s.repositoryProvider(source.ProviderID, organization)
	if err != nil {
		return p, err
	}
	source.Repository = strings.TrimSpace(source.Repository)
	if strings.Contains(source.Repository, "://") {
		u, parseErr := url.Parse(source.Repository)
		base, _ := url.Parse(p.BaseURL)
		if parseErr != nil || u.Scheme != base.Scheme || u.Host != base.Host || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return p, problem(422, "WORKER_REPOSITORY_INVALID", "Repository URL must belong to the selected provider")
		}
		source.Repository = strings.TrimPrefix(u.Path, "/")
	}
	source.Repository = strings.TrimSuffix(strings.TrimSuffix(source.Repository, "/"), ".git")
	if !workerRepositoryNameValid(source.Repository, p.Type) {
		return p, problem(422, "WORKER_REPOSITORY_INVALID", "Use a repository namespace/name")
	}
	if len(p.AllowedRepositories) > 0 && !contains(p.AllowedRepositories, source.Repository) {
		return p, problem(403, "WORKER_REPOSITORY_DENIED", "Repository is not allowed by administrator configuration")
	}
	if source.Ref == "" {
		source.Ref = "HEAD"
	}
	if !repositoryRef.MatchString(source.Ref) || strings.Contains(source.Ref, "..") || strings.HasSuffix(source.Ref, "/") {
		return p, problem(422, "WORKER_REPOSITORY_INVALID", "Use a valid branch, tag or commit")
	}
	if source.RootDirectory == "" {
		source.RootDirectory = "."
	}
	if !safeWorkerRepositoryPath(source.RootDirectory, true) || !safeWorkerRepositoryPath(source.Entrypoint, false) || !contains([]string{".js", ".mjs", ".ts"}, path.Ext(source.Entrypoint)) {
		return p, problem(422, "WORKER_REPOSITORY_INVALID", "Use a relative project directory and JS or TS entrypoint")
	}
	return p, nil
}

func safeWorkerRepositoryPath(name string, allowRoot bool) bool {
	if name == "." {
		return allowRoot
	}
	if name == "" || len(name) > 160 || path.Clean(name) != name || strings.HasPrefix(name, "/") {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if !repositorySegment.MatchString(part) || part == "." || part == ".." || strings.HasPrefix(part, "__krun_") {
			return false
		}
	}
	return true
}

func (s *Server) visibleRepositoryProviders(organization string) []WorkerRepositoryProviderInfo {
	out := make([]WorkerRepositoryProviderInfo, 0)
	for _, p := range s.Config.Workers.RepositoryProviders {
		if !p.Enabled || (len(p.OrganizationIDs) > 0 && !contains(p.OrganizationIDs, organization)) {
			continue
		}
		// Return display metadata only; tokens and repository access policy stay private.
		out = append(out, WorkerRepositoryProviderInfo{ID: p.ID, Type: p.Type, Name: p.Name, Priority: p.Priority, BaseURL: p.BaseURL, CredentialConfigured: p.Token != ""})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Priority == out[j].Priority {
			return out[i].ID < out[j].ID
		}
		return out[i].Priority < out[j].Priority
	})
	return out
}
