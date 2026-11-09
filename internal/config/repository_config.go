package config

import (
	"fmt"
	"net/url"
	"path"
	"regexp"
	slices "slices"
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
	AllowPrivateNetwork bool     `json:"allow_private_network"`
	Token               string   `json:"token"`
	AllowedRepositories []string `json:"allowed_repositories"`
	OrganizationIDs     []string `json:"organization_ids"`
	// OAuth app credentials; when set users connect their own accounts and
	// builds fetch with the connecting user's token instead of a shared PAT.
	ClientID      string `json:"client_id"`
	ClientSecret  string `json:"client_secret"`
	GitHubAppSlug string `json:"github_app_slug"`
}

var RepositorySegment = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_.-]*$`)

var RepositoryRef = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_./-]{0,199}$`)

func (c *WorkersConfig) ValidateRepositoryProviders() error {
	seen := make(map[string]bool)
	for i := range c.RepositoryProviders {
		p := &c.RepositoryProviders[i]
		if !Slug.MatchString(p.ID) || seen[p.ID] || !slices.Contains([]string{"github", "gitlab"}, p.Type) || p.Priority < 0 {
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
		if p.Type == "github" && p.AllowPrivateNetwork {
			return fmt.Errorf("Workers github provider cannot allow private network OAuth endpoints")
		}
		if p.GitHubAppSlug != "" && (p.Type != "github" || !Slug.MatchString(p.GitHubAppSlug) || p.ClientID == "" || p.ClientSecret == "") {
			return fmt.Errorf("Workers GitHub App requires a valid slug and OAuth client credentials")
		}
		if strings.ContainsAny(p.Token, "\r\n") {
			return fmt.Errorf("invalid Workers repository token")
		}
		// A shared private token must have an explicit repository boundary.
		if p.Enabled && p.Token != "" && len(p.AllowedRepositories) == 0 {
			return fmt.Errorf("Workers repository provider %s with a token requires allowed_repositories", p.ID)
		}
		for _, repo := range p.AllowedRepositories {
			if !WorkerRepositoryNameValid(repo, p.Type) {
				return fmt.Errorf("invalid Workers allowed repository for %s", p.ID)
			}
		}
	}
	return nil
}

func WorkerRepositoryNameValid(name, kind string) bool {
	parts := strings.Split(name, "/")
	if len(parts) < 2 || len(parts) > 10 || len(name) > 300 || (kind == "github" && len(parts) != 2) {
		return false
	}
	for _, part := range parts {
		if !RepositorySegment.MatchString(part) || part == "." || part == ".." || strings.HasSuffix(part, ".git") {
			return false
		}
	}
	return true
}

func SafeWorkerRepositoryPath(name string, allowRoot bool) bool {
	if name == "." {
		return allowRoot
	}
	if name == "" || len(name) > 160 || path.Clean(name) != name || strings.HasPrefix(name, "/") {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if !RepositorySegment.MatchString(part) || part == "." || part == ".." || strings.HasPrefix(part, "__krun_") {
			return false
		}
	}
	return true
}
