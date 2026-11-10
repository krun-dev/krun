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
}
