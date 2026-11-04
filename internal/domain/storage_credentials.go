package domain

import "time"

// StorageAccess is an allowlist, never a caller-supplied cloud policy.
type StorageAccess uint8

const (
	StorageRead StorageAccess = 1 << iota
	StorageWrite
	StorageDelete
	StorageList
	StorageMultipart
	StorageAllAccess = StorageRead | StorageWrite | StorageDelete | StorageList | StorageMultipart
)

// StorageCredentialScope must be constructed after authorizing the runtime,
// immutable version, binding and current bucket grant. It is also the complete
// cache identity; revisions prevent reuse after authorization changes.
type StorageCredentialScope struct {
	Organization          string
	BucketID              string
	StoreID               string
	Bucket                string
	Prefix                string
	Access                StorageAccess
	AuthorizationRevision int64
}

// Credentials must never be included in state snapshots or resource responses.
// The trusted-runtime endpoint uses an explicit transport DTO.
type StorageCredentials struct {
	AccessKeyID     string    `json:"-"`
	SecretAccessKey string    `json:"-"`
	SessionToken    string    `json:"-"`
	ExpiresAt       time.Time `json:"-"`
}

func (StorageCredentials) String() string   { return "[storage credentials redacted]" }
func (StorageCredentials) GoString() string { return "[storage credentials redacted]" }
