package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLogReadEndpointLoadsThroughMainConfig(t *testing.T) {
	t.Setenv("KRUN_DATABASE_URL", "postgres://fixture/krun_test")
	for _, endpoint := range []string{"", "https://logs.public.example.com", "https://logs.internal.example.com"} {
		t.Run(endpoint, func(t *testing.T) {
			c, err := LoadConfig(filepath.Join("..", "..", "config", "example.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			upload := c.Workers.S3Endpoint
			c.Workers.S3LogReadEndpoint = endpoint
			raw, _ := json.Marshal(c)
			file := filepath.Join(t.TempDir(), "main.yaml")
			if err := os.WriteFile(file, raw, 0600); err != nil {
				t.Fatal(err)
			}
			loaded, err := LoadConfig(file)
			if err != nil {
				t.Fatal(err)
			}
			expected := endpoint
			if expected == "" {
				expected = upload
			}
			if loaded.Workers.S3Endpoint != upload || loaded.Workers.S3LogReadEndpoint != expected {
				t.Fatal("log endpoint changed upload address or did not load")
			}
		})
	}
}

func TestLogReadEndpointRejectsInvalidOrigins(t *testing.T) {
	t.Setenv("KRUN_DATABASE_URL", "postgres://fixture/krun_test")
	for _, endpoint := range []string{"https://user:secret@example.com", "https://example.com/logs", "https://example.com?token=value", "https://example.com?", "https://example.com#fragment", "file:///logs"} {
		t.Run(endpoint, func(t *testing.T) {
			c, err := LoadConfig(filepath.Join("..", "..", "config", "example.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			c.Workers.S3LogReadEndpoint = endpoint
			raw, _ := json.Marshal(c)
			file := filepath.Join(t.TempDir(), "main.yaml")
			if err := os.WriteFile(file, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadConfig(file); err == nil {
				t.Fatal("invalid log endpoint accepted")
			}
		})
	}
}
