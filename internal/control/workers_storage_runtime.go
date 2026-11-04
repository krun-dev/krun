package control

import (
	"crypto/subtle"
	"net/http"
	"time"
)

func (s *Server) workerRuntimeStorageCredentials(w http.ResponseWriter, r *http.Request) {
	pool, allowed := s.workerRuntimePool(r)
	if !allowed || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+pool.RuntimeToken)) != 1 {
		s.writeError(w, r, forbidden())
		return
	}
	grant, err := s.runtimeApplication().StorageCredentials(r.Context(), r.PathValue("version"), r.PathValue("binding"), pool.ID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	var expiry *time.Time
	if !grant.Credentials.ExpiresAt.IsZero() {
		value := grant.Credentials.ExpiresAt
		expiry = &value
	}
	// This is the only explicit credential serialization path. This DTO must
	// never enter ordinary resource responses or configuration snapshots.
	s.json(w, 200, map[string]any{
		"version_id":             r.PathValue("version"),
		"binding":                r.PathValue("binding"),
		"bucket_id":              grant.BucketID,
		"store_id":               grant.StoreID,
		"endpoint":               grant.Endpoint,
		"region":                 grant.Region,
		"bucket":                 grant.Bucket,
		"request_timeout_ms":     grant.RequestTimeoutMilliseconds,
		"cursor_key":             grant.CursorKey,
		"prefix":                 grant.Prefix,
		"addressing_style":       grant.AddressingStyle,
		"authorization_revision": grant.AuthorizationRevision,
		"lease_until":            grant.LeaseUntil,
		"expires_at":             expiry,
		"access_key_id":          grant.Credentials.AccessKeyID,
		"secret_access_key":      grant.Credentials.SecretAccessKey,
		"session_token":          grant.Credentials.SessionToken,
	})
}
