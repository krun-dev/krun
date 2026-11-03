package storage

import (
	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/storage/models"
)

func sessionModel(hash string, session domain.Session) models.Session {
	return models.Session{SessionHash: hash, UserID: session.User, CSRF: session.CSRF, AuthVersion: session.AuthVersion, CLI: session.CLI, OauthGrantID: session.OAuthGrant, OauthScope: session.OAuthScope, CreatedAt: session.Created, SeenAt: session.Seen, AbsoluteAt: session.Absolute}
}
