package runtimebundle

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/krun-dev/krun/internal/config"
)

func TestRenderedServingAndValidatorStartInRealWorkerd(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "workers", "runtime", "node_modules", ".bin", "workerd")
	if _, err := os.Stat(binary); err != nil {
		t.Skip("install workers/runtime dependencies for actual workerd render validation")
	}
	for _, validator := range []bool{false, true} {
		t.Run(fmt.Sprintf("validator=%t", validator), func(t *testing.T) {
			var requests atomic.Int32
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if strings.Contains(r.URL.Path, "/changes") {
					w.WriteHeader(204)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"pool":"internal-a","config_revision":1,"lazy_routes":true,"routes":[],"versions":{}}`)
			}))
			defer backend.Close()
			cfg := &config.WorkersConfig{
				S3Region:      "us-east-1",
				S3AccessMode:  "signed",
				ConfigBaseURL: backend.URL + "/config",
				ObjectStorage: &config.WorkerObjectStorageConfig{
					Stores: []config.WorkerObjectStore{
						{
							ID:              "data",
							Endpoint:        "https://storage.example.com",
							Bucket:          "fixture-data",
							AddressingStyle: "virtual",
						},
					},
				},
				Domains: []config.WorkerDomain{
					{
						StaticDomain: "static.example.com",
						Access:       "private",
					},
				},
			}
			if validator {
				cfg.RuntimeAPIURL = "https://runtime.example.test"
			}
			pool := config.WorkerRuntimePool{ID: "internal-a", Access: []string{"private"}}
			text, err := Render(cfg, pool, strings.TrimPrefix(backend.URL, "http://"), []string{
				"127.0.0.1/32",
			}, validator)
			if err != nil {
				t.Fatal(err)
			}
			directory := t.TempDir()
			for _, name := range []string{
				"pod/name",
				"pod/uid",
				"secrets/KRUN_RUNTIME_TOKEN",
				"secrets/KRUN_MANAGEMENT_TOKEN",
				"storage/access",
				"storage/secret",
			} {
				target := filepath.Join(directory, name)
				if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(target, []byte("fixture-value"), 0600); err != nil {
					t.Fatal(err)
				}
				text = strings.ReplaceAll(text, `"/`+name+`"`, quote(target))
			}
			text = strings.ReplaceAll(text, `"/app/`, `"`+filepath.ToSlash(filepath.Join(root, "workers", "runtime"))+`/`)
			filename := filepath.Join(directory, "runtime.capnp")
			if err := os.WriteFile(filename, []byte(text), 0600); err != nil {
				t.Fatal(err)
			}
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			address := listener.Addr().String()
			listener.Close()
			args := []string{"serve", filename, "-I", "/", "--experimental", "-s", "management=" + address}
			if !validator {
				args = append(args, "-s", "private=127.0.0.1:0")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			log, err := os.Create(filepath.Join(directory, "workerd.log"))
			if err != nil {
				t.Fatal(err)
			}
			defer log.Close()
			command := exec.CommandContext(ctx, binary, args...)
			command.Stdout = log
			command.Stderr = log
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { cancel(); _ = command.Wait() }()
			client := &http.Client{Timeout: time.Second}
			deadline := time.Now().Add(10 * time.Second)
			for time.Now().Before(deadline) {
				response, err := client.Get("http://" + address + "/ready")
				if err == nil {
					raw, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
					response.Body.Close()
					if response.StatusCode == 200 {
						var status struct{ Pool, Role string }
						if json.Unmarshal(raw, &status) != nil || status.Pool != pool.ID {
							t.Fatalf("wrong runtime identity: %s", raw)
						}
						if validator && (status.Role != "validator" || requests.Load() != 0) {
							t.Fatal("isolated validator bootstrapped serving configuration")
						}
						return
					}
				}
				time.Sleep(50 * time.Millisecond)
			}
			output, _ := os.ReadFile(log.Name())
			t.Fatalf("rendered workerd did not become ready: %s", output)
		})
	}
}

func TestRuntimeAPIURLRendersTLSIdentity(t *testing.T) {
	for _, validator := range []bool{false, true} {
		for _, origin := range []string{"", "https://runtime.example.com:8443"} {
			cfg := &config.WorkersConfig{RuntimeAPIURL: origin}
			text, err := Render(cfg, config.WorkerRuntimePool{ID: "default"}, "control.example.svc:8081", nil, validator)
			if err != nil {
				t.Fatal(err)
			}
			if origin == "" {
				if !strings.Contains(text, `external = (address = "control.example.svc:8081", http = ())`) {
					t.Fatal("default Service transport changed")
				}
				continue
			}
			for _, expected := range []string{
				`address = "runtime.example.com:8443"`, `name = "Host", value = "runtime.example.com:8443"`,
				`certificateHost = "runtime.example.com"`, `trustBrowserCas = true`,
			} {
				if !strings.Contains(text, expected) {
					t.Fatalf("missing TLS configuration %s", expected)
				}
			}
			if strings.Contains(text, "control.example.svc") {
				t.Fatal("explicit origin did not replace Service")
			}
		}
	}
}
