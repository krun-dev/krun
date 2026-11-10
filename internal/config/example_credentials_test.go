package config

import (
	"crypto/rand"
	"encoding/base64"
	"strings"
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

func TestProductionExampleRejectsPublishedCredentials(t *testing.T) {
	setExampleCredentials(t)
	for _, tc := range []struct {
		env   string
		value string
		field string
	}{
		{"KRUN_RUNTIME_TOKEN", "replace-with-serving-runtime-token", "runtime_token"},
		{"KRUN_MANAGEMENT_TOKEN", strings.Repeat("x", 32), "management_token"},
		{"KRUN_WORKERS_SECRETS_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)), "secrets_keys"},
		{"KRUN_OBJECT_STORAGE_CURSOR_KEY", base64.RawURLEncoding.EncodeToString(make([]byte, 32)), "cursor_key"},
	} {
		t.Run(tc.env, func(t *testing.T) {
			t.Setenv(tc.env, tc.value)
			_, err := LoadConfig("../../config/production.example.yaml")
			if err == nil || !strings.Contains(err.Error(), tc.field) {
				t.Fatalf("expected rejection of %s, got %v", tc.field, err)
			}
		})
	}
}
