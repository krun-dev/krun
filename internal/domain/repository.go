package domain

import (
	"time"
)

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

func (c WorkerRepositoryConnection) Status(at time.Time) string {
	if c.ReconnectRequired || c.AccessToken == "" {
		return "reconnect_required"
	}
	if !c.ExpiresAt.IsZero() && !c.ExpiresAt.After(at) {
		if c.RefreshToken == "" {
			return "reconnect_required"
		}
		return "refresh_due"
	}
	return "connected"
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
	ProviderID        string `json:"provider_id"`
	Repository        string `json:"repository"`
	Ref               string `json:"ref"`
	RootDirectory     string `json:"root_directory"`
	AuthorizationUser string `json:"authorization_user,omitempty"`
}

type WorkerBuildRepository struct {
	WorkerRepositorySource
	Commit     string `json:"commit,omitempty"`
	ConfigPath string `json:"config_path,omitempty"`
}
