package repository

import (
	"net/url"
	slices "slices"
	"strings"

	appconfig "github.com/krun-dev/krun/internal/config"
	domain "github.com/krun-dev/krun/internal/domain"
)

func (s *Service) RepositoryProvider(id, organization string) (appconfig.WorkerRepositoryProvider, error) {
	for _, p := range s.Config.Workers.RepositoryProviders {
		// An empty organization is used only after consuming a connect state
		// that was created through an organization-scoped endpoint; that state
		// already proved the provider was allowed there.
		if p.ID == id && p.Enabled && (organization == "" || len(p.OrganizationIDs) == 0 || slices.Contains(p.OrganizationIDs, organization)) {
			return p, nil
		}
	}
	return appconfig.WorkerRepositoryProvider{}, domain.Invalid("WORKER_REPOSITORY_PROVIDER_DISABLED", "Repository provider is not enabled for this organization")
}

func (s *Service) ValidateRepositorySource(source *domain.WorkerRepositorySource, organization string) (appconfig.WorkerRepositoryProvider, error) {
	p, err := s.RepositoryProvider(source.ProviderID, organization)
	if err != nil {
		return p, err
	}
	source.Repository = strings.TrimSpace(source.Repository)
	if strings.Contains(source.Repository, "://") {
		u, parseErr := url.Parse(source.Repository)
		base, _ := url.Parse(p.BaseURL)
		if parseErr != nil || u.Scheme != base.Scheme || u.Host != base.Host || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return p, domain.Invalid("WORKER_REPOSITORY_INVALID", "Repository URL must belong to the selected provider")
		}
		source.Repository = strings.TrimPrefix(u.Path, "/")
	}
	source.Repository = strings.TrimSuffix(strings.TrimSuffix(source.Repository, "/"), ".git")
	if !appconfig.WorkerRepositoryNameValid(source.Repository, p.Type) {
		return p, domain.Invalid("WORKER_REPOSITORY_INVALID", "Use a repository namespace/name")
	}
	if len(p.AllowedRepositories) > 0 && !slices.Contains(p.AllowedRepositories, source.Repository) {
		return p, domain.ForbiddenCode("WORKER_REPOSITORY_DENIED", "Repository is not allowed by administrator configuration")
	}
	if source.Ref == "" {
		source.Ref = "HEAD"
	}
	if !appconfig.RepositoryRef.MatchString(source.Ref) || strings.Contains(source.Ref, "..") || strings.HasSuffix(source.Ref, "/") {
		return p, domain.Invalid("WORKER_REPOSITORY_INVALID", "Use a valid branch, tag or commit")
	}
	if source.RootDirectory == "" {
		source.RootDirectory = "."
	}
	if !appconfig.SafeWorkerRepositoryPath(source.RootDirectory, true) {
		return p, domain.Invalid("WORKER_REPOSITORY_INVALID", "Use a relative project directory")
	}
	return p, nil
}
