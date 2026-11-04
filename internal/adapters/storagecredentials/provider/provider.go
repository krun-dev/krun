// Package provider defines the neutral contract implemented by cloud credential
// issuers. Authorization and caching belong to the caller and Manager.
package provider

import (
	"context"
	"strings"
	"time"
	"unicode"

	"github.com/krun-dev/krun/internal/domain"
)

type Capabilities struct {
	CanIssueTemporary bool
	CanRestrictScope  bool
}

type Provider interface {
	Capabilities() Capabilities
	Retrieve(context.Context, domain.StorageCredentialScope, time.Duration) (domain.StorageCredentials, error)
}

// ValidateScope rejects wildcard injection into IAM resources and conditions.
// Object keys themselves may contain these characters; a platform-owned grant
// prefix may not. Require a trailing slash to distinguish a/ from adjacent a2/.
func ValidateScope(scope domain.StorageCredentialScope) error {
	if scope.Organization == "" || scope.BucketID == "" || scope.StoreID == "" ||
		scope.Bucket == "" || scope.Prefix == "" || !strings.HasSuffix(scope.Prefix, "/") ||
		scope.Access == 0 || scope.Access & ^domain.StorageAllAccess != 0 || scope.AuthorizationRevision < 1 {
		return &Error{Kind: InvalidScope}
	}
	if len(scope.Bucket) > 63 || len(scope.Prefix) > 768 || strings.ContainsAny(scope.Bucket, "/*?[]\\:\r\n") ||
		strings.ContainsAny(scope.Prefix, "*?[]\\") || strings.HasPrefix(scope.Prefix, "/") ||
		strings.IndexFunc(scope.Prefix, unicode.IsControl) >= 0 {
		return &Error{Kind: InvalidScope}
	}
	for _, segment := range strings.Split(strings.TrimSuffix(scope.Prefix, "/"), "/") {
		if segment == "" || segment == "." || segment == ".." {
			return &Error{Kind: InvalidScope}
		}
	}
	return nil
}
