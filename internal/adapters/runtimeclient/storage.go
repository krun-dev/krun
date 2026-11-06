package runtimeclient

import (
	"github.com/krun-dev/krun/internal/domain"
	"time"
)

// ReadGrantPayload is only used by authenticated internal runtime transports.
func ReadGrantPayload(grant domain.StorageReadGrant) map[string]any {
	var expires *time.Time
	if !grant.Credentials.ExpiresAt.IsZero() {
		value := grant.Credentials.ExpiresAt
		expires = &value
	}
	return map[string]any{
		"endpoint":          grant.Endpoint,
		"region":            grant.Region,
		"bucket":            grant.Bucket,
		"prefix":            grant.Prefix,
		"addressing_style":  grant.AddressingStyle,
		"lease_until":       grant.LeaseUntil,
		"expires_at":        expires,
		"access_key_id":     grant.Credentials.AccessKeyID,
		"secret_access_key": grant.Credentials.SecretAccessKey,
		"session_token":     grant.Credentials.SessionToken,
	}
}
