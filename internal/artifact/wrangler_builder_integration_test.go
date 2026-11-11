package artifact_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/krun-dev/krun/internal/adapters/localbuilder"
	workerartifact "github.com/krun-dev/krun/internal/artifact"
	"github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/domain"
	workerapp "github.com/krun-dev/krun/internal/workers"
)

// This is the cross-component contract test: a real pinned Wrangler build
// produces the exact archive that the Go control plane later finalizes. The
// runtime suite independently loads the same profile in a real workerd.
func TestWranglerBuilderArtifactPassesControlPlaneValidation(t *testing.T) {
	files := map[string][]byte{
		"wrangler.jsonc": []byte(`{
  "name": "control-integration",
  "main": "src/index.mjs",
  "compatibility_date": "2026-09-28",
  "rules": [
    {"type":"Text","globs":["**/*.html"],"fallthrough":true},
    {"type":"Data","globs":["**/*.bin"],"fallthrough":true},
    {"type":"CompiledWasm","globs":["**/*.wasm"],"fallthrough":true}
  ],
  "assets": {"directory":"public","binding":"ASSETS"}
}`),
		"src/index.mjs": []byte(`import message from "./message.html";
import payload from "./payload.bin";
import wasm from "./empty.wasm";
export class Named { fetch(){ return new Response("named") } }
export default { fetch(){ return new Response(message + payload.byteLength + typeof wasm) } };`),
		"src/message.html":  []byte("hello"),
		"src/payload.bin":   []byte{0, 1, 2, 3},
		"src/empty.wasm":    []byte{0, 'a', 's', 'm', 1, 0, 0, 0},
		"public/index.html": []byte("<!doctype html><title>integration</title>"),
	}
	code := buildUploadedWranglerFixture(t, files)
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

func TestLocalStaticUploadBuildsWithPinnedWrangler(t *testing.T) {
	html := []byte("<!doctype html><title>Static upload</title>")
	code := buildUploadedWranglerFixture(t, map[string][]byte{
		"index.html":    html,
		"site logo.svg": []byte("<svg xmlns='http://www.w3.org/2000/svg'/>"),
	})
	if code.Assets == nil || len(code.AssetFiles) != 2 ||
		code.AssetFiles["index.html"].Digest != workerartifact.BytesDigest(html) ||
		code.AssetFiles["site logo.svg"].ContentType != "image/svg+xml" {
		t.Fatalf("static artifact lost uploaded files: %+v", code.AssetFiles)
	}
}

func buildUploadedWranglerFixture(t *testing.T, files map[string][]byte) workerartifact.WorkerCode {
	t.Helper()
	builderRoot, err := filepath.Abs("../../workers/builder")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(builderRoot, "node_modules", ".bin", "wrangler")); err != nil {
		t.Skip("workers/builder dependencies are not installed")
	}

	project, err := workerartifact.PrepareLocalProject(nil, files, "")
	if err != nil {
		t.Fatal(err)
	}
	source, err := workerapp.WorkerSourceBundle(project.Files, nil)
	if err != nil {
		t.Fatal(err)
	}
	uploaded := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/source":
			w.Header().Set("Content-Type", "application/gzip")
			_, _ = w.Write(source)
		case "/upload":
			var body bytes.Buffer
			if _, err := body.ReadFrom(http.MaxBytesReader(w, r.Body, workerartifact.WorkerArchiveAssetsMax)); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			uploaded <- body.Bytes()
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cfg := &config.WorkersConfig{
		BuilderScript:         filepath.Join(builderRoot, "build-worker.mjs"),
		BuilderTimeoutSeconds: 120,
	}
	build := domain.WorkerBuild{
		Entrypoint: project.Entrypoint, CompatibilityDate: project.CompatibilityDate,
		CompatibilityFlags: project.CompatibilityFlags,
	}
	private := domain.WorkerBuildPrivate{ConfigPath: project.ConfigPath}
	output, err := localbuilder.Execute(ctx, cfg, build, private, server.URL+"/source", server.URL+"/upload")
	if err != nil {
		t.Fatalf("builder failed: %v\n%s", err, output)
	}
	select {
	case raw := <-uploaded:
		code, _, err := workerartifact.ParseWorkerArchive(raw)
		if err != nil {
			t.Fatalf("control plane rejected Wrangler artifact: %v\n%s", err, output)
		}
		return code
	default:
		t.Fatalf("builder did not upload an artifact\n%s", output)
		return workerartifact.WorkerCode{}
	}
}
