package control

import (
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"
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
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

// WorkerRepositoryConnection is a user's OAuth grant to a repository provider.
type WorkerRepositoryConnection struct {
	User        string    `json:"user"`
	Provider    string    `json:"provider"`
	AccessToken string    `json:"-"`
	ConnectedAt time.Time `json:"connected_at"`

	// OAuth lifecycle fields are credential material used only by the
	// repository store. They are deliberately excluded from JSON so neither
	// generic state snapshots nor API responses can disclose refresh grants.
	TokenType         string    `json:"-"`
	RefreshToken      string    `json:"-"`
	ExpiresAt         time.Time `json:"-"`
	TokenRevision     int64     `json:"-"`
	RefreshState      string    `json:"-"`
	RefreshClaimedAt  time.Time `json:"-"`
	ReconnectRequired bool      `json:"-"`
}

// WorkerRepositoryConnectState is a pending OAuth connect handshake.
type WorkerRepositoryConnectState struct {
	User         string    `json:"user"`
	Provider     string    `json:"provider"`
	Organization string    `json:"organization"`
	Browser      string    `json:"browser"`
	Expires      time.Time `json:"expires"`
}

type WorkerRepositorySource struct {
	ProviderID    string `json:"provider_id"`
	Repository    string `json:"repository"`
	Ref           string `json:"ref"`
	RootDirectory string `json:"root_directory"`
}

type WorkerBuildRepository struct {
	WorkerRepositorySource
	Commit     string `json:"commit,omitempty"`
	ConfigPath string `json:"config_path,omitempty"`
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
		if p.Type == "github" && p.AllowPrivateNetwork {
			return fmt.Errorf("Workers github provider cannot allow private network OAuth endpoints")
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
		// An empty organization is used only after consuming a connect state
		// that was created through an organization-scoped endpoint; that state
		// already proved the provider was allowed there.
		if p.ID == id && p.Enabled && (organization == "" || len(p.OrganizationIDs) == 0 || contains(p.OrganizationIDs, organization)) {
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
	if !safeWorkerRepositoryPath(source.RootDirectory, true) {
		return p, problem(422, "WORKER_REPOSITORY_INVALID", "Use a relative project directory")
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
