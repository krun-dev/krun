package control

import (
	"os"
	"strings"
	"testing"
)

// Consumes a wrangler.jsonc copied verbatim from Cloudflare's official template
// repository (cloudflare/templates, workers-builds-notifications-template), so
// the parser is checked against real-world config rather than a simplification.
func TestParseRealCloudflareTemplateConfig(t *testing.T) {
	raw, err := os.ReadFile("testdata/wrangler.jsonc")
	if err != nil {
		t.Skip("wrangler fixture not present")
	}
	cfg, name, err := parseWranglerConfig(map[string]string{"wrangler.jsonc": string(raw)})
	if err != nil {
		t.Fatal(err)
	}
	if name != "wrangler.jsonc" {
		t.Fatalf("config file: %q", name)
	}
	// The real file carries block comments, a nested assets table and unrelated
	// keys (queues, observability) that must all be tolerated.
	if cfg.Main != "src/index.ts" {
		t.Fatalf("main: %q", cfg.Main)
	}
	if cfg.CompatibilityDate != "2025-10-08" {
		t.Fatalf("compatibility_date: %q", cfg.CompatibilityDate)
	}
	if cfg.Assets == nil || cfg.Assets.Directory != "./public" {
		t.Fatalf("assets: %+v", cfg.Assets)
	}
	// End-to-end resolution: wrangler alone determines entrypoint and assets,
	// with no per-build overrides from the caller.
	assets, entry, err := resolveWranglerBuild(cfg, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if entry != "src/index.ts" {
		t.Fatalf("resolved entrypoint: %q", entry)
	}
	if assets.Directory != "public" {
		t.Fatalf("resolved assets directory: %q", assets.Directory)
	}
}

// A real repository tree carries files that are not part of the module graph:
// type declarations, lockfiles, images. They must not count against the source
// budget or fail the build, while the resolved entrypoint and assets still work.
func TestRepositoryWithRealWorldNoiseFiles(t *testing.T) {
	archive := repositoryTestArchive(t, map[string]string{
		"repo/worker/wrangler.jsonc":            `{"main":"src/index.ts","assets":{"directory":"./public"}}`,
		"repo/worker/src/index.ts":              "export default {fetch(){return new Response('ok')}}",
		"repo/worker/src/helper.ts":             "export const x = 1;",
		"repo/worker/public/index.html":         "<!doctype html><title>ok</title>",
		"repo/worker/worker-configuration.d.ts": "declare const X: string;\n" + strings.Repeat("// types\n", 20000),
		"repo/worker/package-lock.json":         strings.Repeat("{}", 40000),
		"repo/worker/preview.png":               "\x89PNG\r\n\x1a\n",
		"repo/worker/README.md":                 strings.Repeat("# docs\n", 5000),
		"repo/worker/vitest.config.mts":         "export default {};",
	})
	files, _, err := extractRepositorySource(archive, "worker", "")
	if err != nil {
		t.Fatalf("extraction rejected a realistic tree: %v", err)
	}
	cfg, _, err := parseWranglerConfig(files)
	if err != nil || cfg == nil {
		t.Fatalf("config: %v %v", cfg, err)
	}
	assets, entry, err := resolveWranglerBuild(cfg, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if entry != "src/index.ts" || assets.Directory != "public" {
		t.Fatalf("resolved: %q %q", entry, assets.Directory)
	}
	if err := sourceEntryValid(entry, files); err != nil {
		t.Fatalf("entrypoint: %v", err)
	}
	assetFiles, err := extractRepositoryAssets(archive, "worker", assets.Directory)
	if err != nil || len(assetFiles) != 1 {
		t.Fatalf("assets: %v %v", assetFiles, err)
	}
}
