package domain

import (
	"encoding/json"
	"errors"
	"sort"
	"time"
)

func (k APIKey) Active() bool {
	return !k.Revoked && (k.Expires == nil || time.Now().UTC().Before(*k.Expires))
}

func (s *State) RecordAudit(a Audit) {
	s.PendingAudits = append(s.PendingAudits, a)
}

func NewState() *State {
	return &State{
		Workers:                 NewWorkerState(),
		Sources:                 map[string]string{},
		Users:                   map[string]User{},
		Organizations:           map[string]Organization{},
		Members:                 map[string]Member{},
		Groups:                  map[string]Group{},
		Assignments:             map[string]Assignment{},
		Resources:               map[string]Resource{},
		Keys:                    map[string]APIKey{},
		KeyHashes:               map[string]string{},
		Sessions:                map[string]Session{},
		Browsers:                map[string]Browser{},
		Logins:                  map[string]Login{},
		EmailLogins:             map[string]EmailLogin{},
		Devices:                 map[string]Device{},
		Invitations:             map[string]time.Time{},
		RepositoryConnections:   map[string]WorkerRepositoryConnection{},
		RepositoryConnectStates: map[string]WorkerRepositoryConnectState{},
	}
}

func MemberKey(org, user string) string { return org + "/" + user }

func WorkerPlacementKey(service, pool string) string { return service + "\x00" + pool }

func ActiveWorkerPlacement(state *WorkerState, service string) (WorkerPlacement, bool) {
	if state == nil {
		return WorkerPlacement{}, false
	}
	for _, placement := range state.Placements {
		if placement.Service == service && placement.Role == "primary" && placement.State == "active" {
			return placement, true
		}
	}
	return WorkerPlacement{}, false
}

func WorkerServiceAvailableInPool(state *WorkerState, service, pool string) bool {
	if state == nil {
		return false
	}
	placement, ok := state.Placements[WorkerPlacementKey(service, pool)]
	return ok && (placement.State == "preparing" || placement.State == "ready" || placement.State == "active")
}

func NewWorkerState() *WorkerState {
	return &WorkerState{
		Revision:         1,
		Services:         map[string]WorkerService{},
		Placements:       map[string]WorkerPlacement{},
		Builds:           map[string]WorkerBuild{},
		BuildPrivate:     map[string]WorkerBuildPrivate{},
		Artifacts:        map[string]WorkerArtifact{},
		Releases:         map[string]WorkerRelease{},
		Operations:       map[string]WorkerOperation{},
		OperationPrivate: map[string]WorkerOperationPrivate{},
		Histories:        map[string][]WorkerHistory{},
		Idempotency:      map[string]WorkerIdempotency{},
		SecretVersions:   map[string]WorkerSecretVersion{},
	}
}

func (e *APIError) Error() string { return e.Code }

func Problem(status int, code, message string) error {
	return &APIError{Status: status, Code: code, Message: message}
}

func Forbidden() error { return Problem(403, "PERMISSION_DENIED", "Permission denied") }

func NotFound() error { return Problem(404, "RESOURCE_NOT_FOUND", "Resource not found") }

func CloneStringsMap(source map[string]string) map[string]string {
	if source == nil {
		return map[string]string{}
	}
	out := make(map[string]string, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}

func WorkerSecretNames(versions map[string]string) []string {
	names := make([]string, 0, len(versions))
	for name := range versions {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func SameWorkerSecretVersion(a, b WorkerSecretVersion) bool {
	return a.ID == b.ID && a.Organization == b.Organization && a.Service == b.Service &&
		a.Environment == b.Environment && a.Name == b.Name && a.ValueDigest == b.ValueDigest &&
		a.KeyVersion == b.KeyVersion && a.CreatedBy == b.CreatedBy && a.Created.Equal(b.Created)
}

// cloneState deep-copies through JSON, matching the snapshot semantics the
// previous whole-document store provided.
func CloneState(src *State) (*State, error) {
	if src == nil {
		return nil, errors.New("state is not initialized")
	}
	raw, err := json.Marshal(src)
	if err != nil {
		return nil, err
	}
	dst := NewState()
	if err = json.Unmarshal(raw, dst); err != nil {
		return nil, err
	}
	// Secret version references are internal and therefore excluded from JSON.
	// Preserve only the identifiers; ciphertext is never part of State.
	for service, source := range src.Workers.Services {
		target, ok := dst.Workers.Services[service]
		if !ok {
			continue
		}
		for name, sourceEnv := range source.Environments {
			targetEnv, ok := target.Environments[name]
			if !ok {
				continue
			}
			targetEnv.SecretVersions = CloneStringsMap(sourceEnv.SecretVersions)
			target.Environments[name] = targetEnv
		}
		dst.Workers.Services[service] = target
	}
	for id, source := range src.Workers.Releases {
		target, ok := dst.Workers.Releases[id]
		if !ok {
			continue
		}
		target.SecretVersions = CloneStringsMap(source.SecretVersions)
		dst.Workers.Releases[id] = target
	}
	// Refresh tokens and their CAS/lease metadata are intentionally omitted
	// from JSON. Preserve them only for the in-memory test store; PostgreSQL
	// never loads repository credentials into a generic State snapshot.
	for key, source := range src.RepositoryConnections {
		connection, ok := dst.RepositoryConnections[key]
		if !ok {
			continue
		}
		connection.AccessToken = source.AccessToken
		connection.TokenType = source.TokenType
		connection.RefreshToken = source.RefreshToken
		connection.ExpiresAt = source.ExpiresAt
		connection.TokenRevision = source.TokenRevision
		connection.RefreshState = source.RefreshState
		connection.RefreshClaimedAt = source.RefreshClaimedAt
		connection.ReconnectRequired = source.ReconnectRequired
		dst.RepositoryConnections[key] = connection
	}
	// Build lease metadata is also storage-private, but the diff writer needs
	// the exact pre-image for its state/owner CAS.
	for id, source := range src.Workers.Builds {
		build, ok := dst.Workers.Builds[id]
		if !ok {
			continue
		}
		build.ClaimedBy = source.ClaimedBy
		build.ClaimedAt = source.ClaimedAt
		dst.Workers.Builds[id] = build
	}
	return dst, nil
}
