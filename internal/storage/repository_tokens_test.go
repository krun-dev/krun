package storage

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"testing"

	"github.com/krun-dev/krun/internal/domain"
)

func TestRepositoryTokensBindCiphertextAndRetainOldKeyVersions(t *testing.T) {
	keys := RepositoryTokenKeys{
		Active: "old",
		Lookup: func(version string) ([]byte, error) {
			if version != "old" && version != "new" {
				return nil, errors.New("unavailable")
			}
			key := sha256.Sum256([]byte(version))
			return key[:], nil
		},
	}
	connection := domain.WorkerRepositoryConnection{
		User: "user", Provider: "provider", AccessToken: "access fixture", RefreshToken: "refresh fixture",
	}
	access, refresh, err := keys.seal(connection)
	if err != nil {
		t.Fatal(err)
	}
	another, _, err := keys.seal(connection)
	if err != nil || bytes.Equal(another, access) {
		t.Fatal("sealing the same token should produce distinct ciphertext")
	}
	keys.Active = "new"

	opened := domain.WorkerRepositoryConnection{User: connection.User, Provider: connection.Provider}
	if err := keys.open(&opened, "old", access, refresh); err != nil ||
		opened.AccessToken != connection.AccessToken || opened.RefreshToken != connection.RefreshToken {
		t.Fatalf("old key version could not open its tokens: %v", err)
	}

	for _, tc := range []struct {
		user, provider, version string
		ciphertext              []byte
	}{
		{"other-user", connection.Provider, "old", access},
		{connection.User, "other-provider", "old", access},
		{connection.User, connection.Provider, "new", access},
		{connection.User, connection.Provider, "removed", access},
		{connection.User, connection.Provider, "old", refresh},
	} {
		opened := domain.WorkerRepositoryConnection{User: tc.user, Provider: tc.provider}
		if err := keys.open(&opened, tc.version, tc.ciphertext, nil); err == nil {
			t.Fatal("unexpectedly opened a token outside its original context")
		}
	}
	if _, _, err := (RepositoryTokenKeys{}).seal(connection); err == nil {
		t.Fatal("token write accepted without a key")
	}
}
