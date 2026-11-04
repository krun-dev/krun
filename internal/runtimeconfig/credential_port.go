package runtimeconfig

import (
	"context"
	"time"

	"github.com/krun-dev/krun/internal/domain"
)

// CredentialSource only receives scopes already authorized by this application.
type CredentialSource interface {
	Credentials(context.Context, domain.StorageCredentialScope, time.Duration) (domain.StorageCredentials, error)
}
