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

// StorageCredentialScope must be constructed after authorizing the runtime
// and its target resource (binding, artifact or static origin). It is the complete
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

// StorageReadGrant stays inside trusted runtime transports. It is not part of
// a persisted version, public API response or configuration snapshot.
type StorageReadGrant struct {
	Endpoint        string             `json:"-"`
	Region          string             `json:"-"`
	Bucket          string             `json:"-"`
	Prefix          string             `json:"-"`
	AddressingStyle string             `json:"-"`
	LeaseUntil      time.Time          `json:"-"`
	Credentials     StorageCredentials `json:"-"`
}
