package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestRuntimeAPIURLLoadsThroughMainConfig(t *testing.T) {
	t.Setenv("KRUN_DATABASE_URL", "postgres://fixture/krun_test")
	for _, origin := range []string{"", "https://runtime.example.com/", "https://runtime.example.com:8443"} {
		t.Run(origin, func(t *testing.T) {
			c, err := LoadConfig(filepath.Join("..", "..", "config", "example.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			c.Workers.RuntimeAPIURL = origin
			raw, err := json.Marshal(c)
			if err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(t.TempDir(), "main.yaml")
			if err := os.WriteFile(file, raw, 0600); err != nil {
				t.Fatal(err)
			}
			loaded, err := LoadConfig(file)
			if err != nil {
				t.Fatal(err)
			}
			expected := origin
			if origin == "https://runtime.example.com/" {
				expected = "https://runtime.example.com"
			}
			if loaded.Workers.RuntimeAPIURL != expected {
				t.Fatal("runtime origin was not loaded/normalized")
			}
		})
	}
}

func TestRuntimeAPIURLRejectsUnsafeOrigins(t *testing.T) {
	for _, value := range []string{
		"http://runtime.example.com", "https://user:password@runtime.example.com", "https://runtime.example.com/api",
		"https://runtime.example.com/?token=credential", "https://runtime.example.com?", "https://runtime.example.com/#fragment",
		"https://runtime.example.com:0", "https://runtime.example.com:65536", "https://runtime.example.com:%22", "https:///", "runtime.example.com",
	} {
		t.Run(value, func(t *testing.T) {
			c := WorkersConfig{RuntimeAPIURL: value}
			if _, err := c.RuntimeAPIOrigin(); err == nil {
				t.Fatal("unsafe origin accepted")
			}
		})
	}
}
