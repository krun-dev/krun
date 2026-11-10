package storage

import (
	"crypto/aes"
	"crypto/cipher"
	"errors"
	"strings"

	"github.com/krun-dev/krun/internal/domain"
)

type RepositoryTokenKeys struct {
	Active string
	Lookup func(version string) ([]byte, error)
}

func (k RepositoryTokenKeys) cipher(version string) (cipher.AEAD, error) {
	if version == "" || k.Lookup == nil {
		return nil, errors.New("repository token encryption key is not configured")
	}
	key, err := k.Lookup(version)
	if err != nil || len(key) != 32 {
		return nil, errors.New("repository token encryption key version is unavailable")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCMWithRandomNonce(block)
}

func repositoryTokenAAD(connection domain.WorkerRepositoryConnection, version, field string) []byte {
	return []byte(strings.Join([]string{
		"krun/repository-token/v1", connection.User, connection.Provider, version, field,
	}, "\x00"))
}

func (k RepositoryTokenKeys) seal(connection domain.WorkerRepositoryConnection) (access, refresh []byte, err error) {
	aead, err := k.cipher(k.Active)
	if err != nil {
		return nil, nil, err
	}
	if connection.AccessToken != "" {
		access = aead.Seal(nil, nil, []byte(connection.AccessToken), repositoryTokenAAD(connection, k.Active, "access"))
	}
	if connection.RefreshToken != "" {
		refresh = aead.Seal(nil, nil, []byte(connection.RefreshToken), repositoryTokenAAD(connection, k.Active, "refresh"))
	}
	return access, refresh, nil
}

func (k RepositoryTokenKeys) open(connection *domain.WorkerRepositoryConnection, version string, access, refresh []byte) error {
	if access == nil && refresh == nil {
		return nil
	}
	aead, err := k.cipher(version)
	if err != nil {
		return err
	}
	for _, token := range []struct {
		field      string
		ciphertext []byte
		value      *string
	}{
		{"access", access, &connection.AccessToken},
		{"refresh", refresh, &connection.RefreshToken},
	} {
		if token.ciphertext == nil {
			continue
		}
		value, err := aead.Open(nil, nil, token.ciphertext, repositoryTokenAAD(*connection, version, token.field))
		if err != nil {
			return errors.New("repository token authentication failed")
		}
		*token.value = string(value)
	}
	return nil
}
