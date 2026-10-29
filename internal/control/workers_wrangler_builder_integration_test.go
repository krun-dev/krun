package control

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// This is the cross-component contract test: a real pinned Wrangler build
// produces the exact archive that the Go control plane later finalizes. The
// runtime suite independently loads the same profile in a real workerd.
func TestWranglerBuilderArtifactPassesControlPlaneValidation(t *testing.T) {
	builderRoot, err := filepath.Abs("../../workers/builder")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(builderRoot, "node_modules", ".bin", "wrangler")); err != nil {
		t.Skip("workers/builder dependencies are not installed")
	}

	project := map[string]string{
		"wrangler.jsonc": `{
  "name": "control-integration",
  "main": "src/index.mjs",
  "compatibility_date": "2026-09-28",
  "rules": [
    {"type":"Text","globs":["**/*.html"],"fallthrough":true},
    {"type":"Data","globs":["**/*.bin"],"fallthrough":true},
    {"type":"CompiledWasm","globs":["**/*.wasm"],"fallthrough":true}
  ],
  "assets": {"directory":"public","binding":"ASSETS"}
}`,
		"src/index.mjs": `import message from "./message.html";
import payload from "./payload.bin";
import wasm from "./empty.wasm";
export class Named { fetch(){ return new Response("named") } }
export default { fetch(){ return new Response(message + payload.byteLength + typeof wasm) } };`,
		"src/message.html":  "hello",
		"src/payload.bin":   string([]byte{0, 1, 2, 3}),
		"src/empty.wasm":    string([]byte{0, 'a', 's', 'm', 1, 0, 0, 0}),
		"public/index.html": "<!doctype html><title>integration</title>",
	}
	source, err := workerSourceBundle(project, nil)
	if err != nil {
		t.Fatal(err)
	}
	var uploaded []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/source":
			w.Header().Set("Content-Type", "application/gzip")
			_, _ = w.Write(source)
		case "/upload":
			defer r.Body.Close()
			var body bytes.Buffer
			if _, err := body.ReadFrom(http.MaxBytesReader(w, r.Body, workerArchiveAssetsMax)); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			uploaded = append([]byte(nil), body.Bytes()...)
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	temp := t.TempDir()
	inputPath := filepath.Join(temp, "input.json")
	work := filepath.Join(temp, "work")
	if err := os.MkdirAll(work, 0700); err != nil {
		t.Fatal(err)
	}
	input, err := json.Marshal(map[string]any{
		"build_mode":          "wrangler",
		"config_path":         "wrangler.jsonc",
		"source_url":          server.URL + "/source",
		"upload_url":          server.URL + "/upload",
		"compatibility_date":  workerDefaultCompatibilityDate,
		"compatibility_flags": []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inputPath, input, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "build-worker.mjs")
	cmd.Dir = builderRoot
	cmd.Env = localBuilderEnvironment(inputPath, work, "https://registry.npmjs.org/", filepath.Join(temp, "home"), filepath.Join(temp, "tmp"), 120)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("builder failed: %v\n%s", err, output)
	}
	if len(uploaded) == 0 {
		t.Fatalf("builder did not upload an artifact\n%s", output)
	}
	code, _, err := parseWorkerArchive(uploaded)
	if err != nil {
		t.Fatalf("control plane rejected Wrangler artifact: %v\n%s", err, output)
	}
	if code.MainModule != "index.js" || code.Assets == nil || code.Assets.Binding != "ASSETS" || len(code.AssetFiles) != 1 {
		t.Fatalf("artifact metadata was not preserved: main=%q assets=%+v files=%d", code.MainModule, code.Assets, len(code.AssetFiles))
	}
	types := map[string]bool{}
	for _, module := range code.Modules {
		types[module.Type] = true
	}
	for _, kind := range []string{"esm", "text", "data", "wasm"} {
		if !types[kind] {
			t.Fatalf("Wrangler artifact lost %s module: %#v", kind, types)
		}
	}
}
