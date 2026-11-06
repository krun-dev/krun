package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestValidationModeLoadsThroughMainConfig(t *testing.T) {
	t.Setenv("KRUN_DATABASE_URL", "postgres://fixture/krun_test")
	for _, selected := range []string{"", "public-a"} {
		t.Run("validator_pool="+selected, func(t *testing.T) {
			c, err := LoadConfig(filepath.Join("..", "..", "config", "example.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			c.Workers.ValidatorPool = selected
			if selected == "" {
				c.Workers.RuntimePools[0].ValidatorService = ""
			}
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
			if loaded.Workers.ValidatorPool != selected {
				t.Fatal("validation selection did not survive LoadConfig")
			}
		})
	}
}
