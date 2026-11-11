package control

import (
	"crypto/rand"
	"encoding/base64"
	"testing"
)

func setExampleCredentials(t *testing.T) {
	t.Helper()
	for _, prefix := range []string{"KRUN_", "KRUN_INTERNAL_", "KRUN_OFFICE_", "KRUN_PUBLIC_"} {
		t.Setenv(prefix+"RUNTIME_TOKEN", rand.Text())
		t.Setenv(prefix+"MANAGEMENT_TOKEN", rand.Text())
	}
	var key [32]byte
	rand.Read(key[:])
	t.Setenv("KRUN_WORKERS_SECRETS_KEY", base64.StdEncoding.EncodeToString(key[:]))
	rand.Read(key[:])
	t.Setenv("KRUN_OBJECT_STORAGE_CURSOR_KEY", base64.RawURLEncoding.EncodeToString(key[:]))
	// Hyperdrive is required whenever workers: is configured; the examples
	// reference these placeholders, so tests must supply valid values.
	t.Setenv("KRUN_HYPERDRIVE_LISTEN", "10.10.0.10:6432")
	t.Setenv("KRUN_HYPERDRIVE_ADDRESS", "hyperdrive.workers.example.com:6432")
	t.Setenv("KRUN_HYPERDRIVE_CERT_FILE", "/fixture/hyperdrive.crt")
	t.Setenv("KRUN_HYPERDRIVE_KEY_FILE", "/fixture/hyperdrive.key")
}
