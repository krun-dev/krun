package repository

import (
	slices "slices"

	repooauth "github.com/krun-dev/krun/internal/adapters/repositoryoauth"
	appconfig "github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/securetoken"
)

// repositoryOAuthContext confines client credentials to the configured token
// endpoint. Redirects are rejected before net/http can replay a POST carrying
// client_secret, and the transport cannot dial a different host or port.

type RepositoryConnectionInfo struct {
	ID                  string `json:"id"`
	Type                string `json:"type"`
	Name                string `json:"name"`
	Priority            int    `json:"priority"`
	BaseURL             string `json:"base_url"`
	Connected           bool   `json:"connected"`
	OAuth               bool   `json:"oauth"`
	AuthorizationStatus string `json:"authorization_status"`
	InstallURL          string `json:"install_url,omitempty"`
}

func (s *Service) VisibleRepositoryProviders(organization string, statuses map[string]string) []RepositoryConnectionInfo {
	out := make([]RepositoryConnectionInfo, 0)
	for _, p := range s.Config.Workers.RepositoryProviders {
		if !p.Enabled || (len(p.OrganizationIDs) > 0 && !slices.Contains(p.OrganizationIDs, organization)) {
			continue
		}
		info := RepositoryConnectionInfo{ID: p.ID, Type: p.Type, Name: p.Name, Priority: p.Priority, BaseURL: p.BaseURL, OAuth: p.ClientID != ""}
		info.AuthorizationStatus = AuthorizationStatus(p, statuses)
		if info.AuthorizationStatus == "" {
			info.AuthorizationStatus = "not_connected"
		}
		info.Connected = info.AuthorizationStatus == "connected" || info.AuthorizationStatus == "refresh_due"
		if p.GitHubAppSlug != "" {
			info.InstallURL = "https://github.com/apps/" + p.GitHubAppSlug + "/installations/new"
		}
		out = append(out, info)
	}

	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && (out[j].Priority < out[j-1].Priority || (out[j].Priority == out[j-1].Priority && out[j].ID < out[j-1].ID)); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

const RepositoryOAuthResponseLimitBytes = repooauth.RepositoryOAuthResponseLimitBytes

// Keep a GitHub App's grants separate from broader OAuth App grants, and
// require reauthorization if the configured GitHub App registration changes.
func authorizationKey(provider appconfig.WorkerRepositoryProvider) string {
	if provider.GitHubAppSlug == "" {
		return provider.ID
	}
	return provider.ID + ":github-app:" + securetoken.Digest(provider.ClientID)
}

func AuthorizationStatus(provider appconfig.WorkerRepositoryProvider, statuses map[string]string) string {
	return statuses[authorizationKey(provider)]
}
