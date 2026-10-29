package control

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func workerAssetArchive(t *testing.T, manifest map[string]any, assets map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	write := func(name string, data []byte) {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := json.Marshal(manifest)
	write("manifest.json", raw)
	write("modules/index.js", []byte("export default {fetch(){return new Response('ok')}}"))
	for name, data := range assets {
		write("assets/"+name, data)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func workerAssetManifest(files []map[string]any, options map[string]any) map[string]any {
	assets := map[string]any{"directory": "public", "run_worker_first": []string{"/api/*"}, "not_found_handling": "single-page-application", "html_handling": "auto-trailing-slash", "files": files}
	if options != nil {
		for k, v := range options {
			assets[k] = v
		}
	}
	return map[string]any{"schema_version": 2, "profile": workerProfile, "compatibility_date": workerDefaultCompatibilityDate, "main_module": "index.js",
		"modules": []map[string]any{{"name": "index.js", "path": "modules/index.js", "type": "esm", "sha256": bytesDigest([]byte("export default {fetch(){return new Response('ok')}}")), "size_bytes": len("export default {fetch(){return new Response('ok')}}")}},
		"assets":  assets}
}

func TestParseWorkerAssets(t *testing.T) {
	html := []byte("<!doctype html><title>app</title>")
	js := []byte("console.log('app')")
	license := []byte("fixture license")
	manifest := workerAssetManifest([]map[string]any{
		{"path": "index.html", "sha256": bytesDigest(html), "size_bytes": len(html), "content_type": "text/html; charset=utf-8"},
		{"path": "assets/app-0123abcd.js", "sha256": bytesDigest(js), "size_bytes": len(js), "content_type": "application/javascript; charset=utf-8"},
		{"path": "LICENSE", "sha256": bytesDigest(license), "size_bytes": len(license), "content_type": "application/octet-stream"},
	}, nil)
	code, _, err := parseWorkerArchive(workerAssetArchive(t, manifest, map[string][]byte{"index.html": html, "assets/app-0123abcd.js": js, "LICENSE": license}))
	if err != nil {
		t.Fatal(err)
	}
	if code.Assets == nil || len(code.AssetFiles) != 3 || code.Assets.NotFoundHandling != "single-page-application" {
		t.Fatal("assets were not parsed", code.Assets, len(code.AssetFiles))
	}
	if code.AssetFiles["index.html"].ContentType != "text/html; charset=utf-8" {
		t.Fatal("content type lost")
	}
	if code.AssetFiles["LICENSE"].ContentType != "application/octet-stream" {
		t.Fatal("extensionless asset did not use the opaque fallback")
	}
}

func TestParseWorkerAssetsRejections(t *testing.T) {
	html := []byte("<!doctype html>")
	bad := []byte("binary")
	if _, _, err := parseWorkerArchive(workerAssetArchive(t, workerAssetManifest([]map[string]any{
		{"path": "index.html", "sha256": bytesDigest(html), "size_bytes": len(html), "content_type": "text/html; charset=utf-8"},
		{"path": "payload.exe", "sha256": bytesDigest(bad), "size_bytes": len(bad), "content_type": "text/html; charset=utf-8"},
	}, nil), map[string][]byte{"index.html": html, "payload.exe": bad})); err == nil {
		t.Fatal("unknown extension accepted a spoofed content type")
	}
	if _, _, err := parseWorkerArchive(workerAssetArchive(t, workerAssetManifest([]map[string]any{
		{"path": "index.html", "sha256": bytesDigest([]byte("tampered")), "size_bytes": len(html), "content_type": "text/html; charset=utf-8"},
	}, nil), map[string][]byte{"index.html": html})); err == nil {
		t.Fatal("digest mismatch accepted")
	}
	if _, _, err := parseWorkerArchive(workerAssetArchive(t, workerAssetManifest([]map[string]any{
		{"path": "app.js", "sha256": bytesDigest(html), "size_bytes": len(html), "content_type": "application/javascript; charset=utf-8"},
	}, nil), map[string][]byte{"app.js": html})); err == nil || !strings.Contains(err.Error(), "single-page-application") {
		t.Fatalf("SPA without index.html accepted: %v", err)
	}
	if _, _, err := parseWorkerArchive(workerAssetArchive(t, workerAssetManifest(nil, map[string]any{"run_worker_first": []string{"/api/*/x"}}), map[string][]byte{"index.html": html})); err == nil {
		t.Fatal("invalid pattern accepted")
	}
}

// Consumes the archive produced by the real builder (workers/builder/build-worker.mjs,
// see workers/builder/test/archive.test.mjs) so the Go parser and the JS producer
// cannot drift apart silently. Skips when the fixture is absent.
func TestParseBuilderProducedArchive(t *testing.T) {
	raw, err := os.ReadFile("testdata/builder-archive.tar.gz")
	if err != nil {
		t.Skip("builder fixture not present")
	}
	code, _, err := parseWorkerArchive(raw)
	if err != nil {
		t.Fatal(err)
	}
	if code.Assets == nil || code.Assets.Directory != "public" || len(code.AssetFiles) != 4 {
		t.Fatalf("unexpected assets: %+v files=%d", code.Assets, len(code.AssetFiles))
	}
	for name, meta := range code.AssetFiles {
		if len(code.AssetBytes[name]) != meta.Size || meta.ContentType == "" {
			t.Fatalf("bad asset metadata for %s", name)
		}
	}
}

func TestSchemaOneArchiveStillParses(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	code := []byte("export default {fetch(){return new Response('ok')}}")
	manifest, _ := json.Marshal(map[string]any{"schema_version": 1, "profile": workerProfile, "compatibility_date": workerDefaultCompatibilityDate, "main_module": "index.js",
		"modules": []map[string]any{{"name": "index.js", "path": "modules/index.js", "type": "esm", "sha256": bytesDigest(code), "size_bytes": len(code)}}})
	for name, data := range map[string][]byte{"manifest.json": manifest, "modules/index.js": code} {
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg})
		_, _ = tw.Write(data)
	}
	_ = tw.Close()
	_ = gz.Close()
	parsed, _, err := parseWorkerArchive(buf.Bytes())
	if err != nil || parsed.Assets != nil {
		t.Fatal(parsed, err)
	}
}
