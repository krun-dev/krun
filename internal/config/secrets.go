package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

func (c *WorkersConfig) ValidateSecretKeys() error {
	if len(c.SecretsKeys) == 0 && c.SecretsActiveKey == "" {
		return nil
	}
	if c.SecretsActiveKey == "" || !Slug.MatchString(c.SecretsActiveKey) {
		return fmt.Errorf("valid Workers secrets_active_key required")
	}
	for version, encoded := range c.SecretsKeys {
		key, err := base64.StdEncoding.DecodeString(encoded)
		if !Slug.MatchString(version) || err != nil || len(key) != 32 || weakCredential(string(key)) {
			return fmt.Errorf("Workers secrets_keys must map valid versions to random base64-encoded 32-byte keys")
		}
	}
	if _, ok := c.SecretsKeys[c.SecretsActiveKey]; !ok {
		return fmt.Errorf("Workers secrets_active_key is absent from secrets_keys")
	}
	return nil
}

func (c *WorkersConfig) WorkerSecretKey(version string) ([]byte, error) {
	if c == nil || version == "" || len(c.SecretsKeys) == 0 {
		return nil, errors.New("Workers secrets are not configured")
	}
	encoded, ok := c.SecretsKeys[version]
	if !ok {
		return nil, errors.New("Workers secret key version unavailable")
	}
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(key) != 32 {
		return nil, errors.New("Workers secret key is invalid")
	}
	return key, nil
}

// Catch published placeholders and obviously predictable values at startup.
func weakCredential(value string) bool {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(value)), "replace-") {
		return true
	}
	var seen [256]bool
	distinct := 0
	for i := range len(value) {
		if !seen[value[i]] {
			seen[value[i]] = true
			distinct++
		}
	}
	return distinct < 8
}
