package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestMergedDeploymentLoadsThroughMainConfig(t *testing.T) {
	t.Setenv("KRUN_DATABASE_URL", "postgres://fixture/krun_test")
	c, err := LoadConfig(filepath.Join("..", "..", "config", "example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if c.MergedDeployment {
		t.Fatal("merged deployment must be opt-in")
	}
	c.MergedDeployment = true
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
	if !loaded.MergedDeployment {
		t.Fatal("merged deployment did not survive LoadConfig")
	}
	for _, address := range []string{"", "127.0.0.1:0", "127.0.0.1:http", "127.0.0.1:65536"} {
		invalid := loaded
		invalid.InternalListen = address
		if err := invalid.Validate(); err == nil {
			t.Fatalf("invalid merged internal listener %s accepted", address)
		}
	}
	invalid := loaded
	invalid.Workers = nil
	if err := invalid.Validate(); err == nil {
		t.Fatal("merged deployment without Workers accepted")
	}
}
