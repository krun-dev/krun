package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKubeconfigSettingsLoadThroughMainConfig(t *testing.T) {
	setExampleCredentials(t)
	t.Setenv("KRUN_DATABASE_URL", "postgres://fixture/krun_test")
	c, err := LoadConfig(filepath.Join("..", "..", "config", "example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	c.Workers.KubernetesAPI = ""
	c.Workers.KubernetesTokenFile = ""
	c.Workers.KubernetesCAFile = ""
	c.Workers.KubernetesKubeconfigFile = filepath.Join(t.TempDir(), "credentials")
	c.Workers.KubernetesContext = "test-cluster"
	// Loading the main config is offline; credential file access belongs to the adapter.
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
	w := loaded.Workers
	if w.KubernetesKubeconfigFile != c.Workers.KubernetesKubeconfigFile || w.KubernetesContext != "test-cluster" {
		t.Fatal("kubeconfig selection did not survive LoadConfig")
	}
	if w.KubernetesAPI != "" || w.KubernetesTokenFile != "" || w.KubernetesCAFile != "" {
		t.Fatal("service-account defaults contaminated kubeconfig mode")
	}
	if err := loaded.Validate(); err != nil {
		t.Fatalf("validation is not idempotent: %v", err)
	}
}

func TestKubernetesCredentialSourcesCannotBeAmbiguous(t *testing.T) {
	file := filepath.Join(t.TempDir(), "kubeconfig")
	for _, test := range []struct {
		name    string
		config  WorkersConfig
		message string
	}{
		{name: "relative kubeconfig", config: WorkersConfig{KubernetesKubeconfigFile: "relative/config"}, message: "absolute path"},
		{name: "context without file", config: WorkersConfig{KubernetesContext: "test"}, message: "requires"},
		{name: "API override", config: WorkersConfig{KubernetesKubeconfigFile: file, KubernetesAPI: "https://cluster.example.com"}, message: "cannot be combined"},
		{name: "token override", config: WorkersConfig{KubernetesKubeconfigFile: file, KubernetesTokenFile: "/credentials/token"}, message: "cannot be combined"},
		{name: "CA override", config: WorkersConfig{KubernetesKubeconfigFile: file, KubernetesCAFile: "/credentials/ca"}, message: "cannot be combined"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := test.config.validateKubernetes()
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("credential selection: %v", err)
			}
		})
	}
}

func TestKubernetesServiceAccountDefaultsRemainAvailable(t *testing.T) {
	var w WorkersConfig
	if err := w.validateKubernetes(); err != nil {
		t.Fatal(err)
	}
	if w.KubernetesAPI != "https://kubernetes.default.svc" || w.KubernetesTokenFile != "/var/run/secrets/kubernetes.io/serviceaccount/token" || w.KubernetesCAFile != "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt" {
		t.Fatal("service-account defaults changed")
	}
}
