package control

import (
	"encoding/json"
	"testing"
)

func TestParseWranglerJSONC(t *testing.T) {
	raw := `{
  // wrangler.jsonc is the recommended format; comments must be legal input.
  "name": "my-worker",
  "main": "src/index.ts",
  "compatibility_date": "2026-09-28",
  "compatibility_flags": ["nodejs_compat"],
  "assets": {
    "directory": "./public",
    "binding": "ASSETS",
    "run_worker_first": ["/api/*", "!/api/health"],
    "not_found_handling": "single-page-application",
    "html_handling": "auto-trailing-slash", // trailing comma below
  },
}`
	cfg, name, err := parseWranglerConfig(map[string]string{"wrangler.jsonc": raw})
	if err != nil {
		t.Fatal(err)
	}
	if name != "wrangler.jsonc" {
		t.Fatalf("config file: %q", name)
	}
	if cfg.Main != "src/index.ts" || cfg.CompatibilityDate != "2026-09-28" || len(cfg.CompatibilityFlags) != 1 {
		t.Fatalf("core fields: %+v", cfg)
	}
	if cfg.Assets == nil || cfg.Assets.Directory != "./public" || cfg.Assets.Binding != "ASSETS" || cfg.Assets.NotFoundHandling != "single-page-application" || cfg.Assets.HTMLHandling != "auto-trailing-slash" {
		t.Fatalf("assets: %+v", cfg.Assets)
	}
	var flags []string
	if err := json.Unmarshal(cfg.Assets.RunWorkerFirst, &flags); err != nil || len(flags) != 2 || flags[1] != "!/api/health" {
		t.Fatalf("run_worker_first: %s %v", cfg.Assets.RunWorkerFirst, err)
	}
}

func TestParseWranglerTOML(t *testing.T) {
	raw := `
name = "my-worker"
main = "src/index.ts"
compatibility_date = "2026-09-28"
compatibility_flags = [
  "nodejs_compat",
]

[assets]
directory = "./public"
binding = "ASSETS"
run_worker_first = ["/api/*"]
not_found_handling = "404-page"
html_handling = "none"
`
	cfg, name, err := parseWranglerConfig(map[string]string{"wrangler.toml": raw})
	if err != nil {
		t.Fatal(err)
	}
	if name != "wrangler.toml" || cfg.Main != "src/index.ts" || len(cfg.CompatibilityFlags) != 1 {
		t.Fatalf("config: %q %+v", name, cfg)
	}
	if cfg.Assets == nil || cfg.Assets.Directory != "./public" || cfg.Assets.Binding != "ASSETS" || cfg.Assets.NotFoundHandling != "404-page" || cfg.Assets.HTMLHandling != "none" {
		t.Fatalf("assets: %+v", cfg.Assets)
	}
	var flags []string
	if err := json.Unmarshal(cfg.Assets.RunWorkerFirst, &flags); err != nil || len(flags) != 1 || flags[0] != "/api/*" {
		t.Fatalf("run_worker_first: %s", cfg.Assets.RunWorkerFirst)
	}
}

func TestParseWranglerTOMLBooleanRunWorkerFirst(t *testing.T) {
	raw := "main = \"index.ts\"\n\n[assets]\ndirectory = \"dist\"\nrun_worker_first = true\n"
	cfg, _, err := parseWranglerConfig(map[string]string{"wrangler.toml": raw})
	if err != nil {
		t.Fatal(err)
	}
	assets, entry, err := resolveWranglerBuild(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if entry != "index.ts" || assets.Directory != "dist" {
		t.Fatalf("resolved: %q %+v", entry, assets)
	}
	// wrangler's `true` means every request goes through the Worker first.
	if len(assets.RunWorkerFirst) != 1 || assets.RunWorkerFirst[0] != "/*" {
		t.Fatalf("run_worker_first boolean: %v", assets.RunWorkerFirst)
	}
}

func TestResolveWranglerCompatibility(t *testing.T) {
	date, flags, err := resolveWranglerCompatibility(&wranglerConfig{
		CompatibilityDate:  "2026-09-28",
		CompatibilityFlags: []string{"nodejs_compat", "enable_nodejs_fs_module"},
	})
	if err != nil || date != "2026-09-28" || len(flags) != 2 || flags[0] != "enable_nodejs_fs_module" {
		t.Fatalf("resolved compatibility: %q %v %v", date, flags, err)
	}
	date, flags, err = resolveWranglerCompatibility(nil)
	if err != nil || date != workerDefaultCompatibilityDate || len(flags) != 0 {
		t.Fatalf("default compatibility: %q %v %v", date, flags, err)
	}
	for _, cfg := range []*wranglerConfig{
		{CompatibilityDate: "2026-02-30"},
		{CompatibilityDate: "2021-11-01"},
		{CompatibilityDate: "2026-09-29"},
		{CompatibilityFlags: []string{"nodejs-compat"}},
		{CompatibilityFlags: []string{"nodejs_compat", "nodejs_compat"}},
	} {
		if _, _, err := resolveWranglerCompatibility(cfg); err == nil {
			t.Fatalf("invalid compatibility accepted: %+v", cfg)
		}
	}
}

func TestParseWranglerPrecedenceAndAbsence(t *testing.T) {
	files := map[string]string{
		"wrangler.json": `{"main":"a.ts"}`,
		"wrangler.toml": "main = \"b.ts\"\n",
	}
	cfg, name, err := parseWranglerConfig(files)
	if err != nil {
		t.Fatal(err)
	}
	if name != "wrangler.json" || cfg.Main != "a.ts" {
		t.Fatalf("json should win over toml: %q %+v", name, cfg)
	}
	// No config at all is not an error: the platform's own layout still works.
	cfg, name, err = parseWranglerConfig(map[string]string{"index.ts": "export default {}"})
	if err != nil || cfg != nil || name != "" {
		t.Fatalf("absent config: %v %q %v", cfg, name, err)
	}
}

func TestResolveWranglerBuildRejectsEscapes(t *testing.T) {
	for _, cfg := range []*wranglerConfig{
		{Main: "../outside.ts"},
		{Assets: &wranglerAssets{Directory: "../outside"}},
	} {
		if _, _, err := resolveWranglerBuild(cfg); err == nil {
			t.Fatalf("escape accepted: %+v", cfg)
		}
	}
}

func TestResolveWranglerBuildNormalisesPaths(t *testing.T) {
	cfg := &wranglerConfig{Main: "./src/index.ts", Assets: &wranglerAssets{Directory: "./public/"}}
	assets, entry, err := resolveWranglerBuild(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if entry != "src/index.ts" || assets.Directory != "public" {
		t.Fatalf("normalisation: %q %q", entry, assets.Directory)
	}
}

func TestValidateWranglerInputAssetsAllowsGeneratedDirectory(t *testing.T) {
	if err := validateWranglerInputAssets(WorkerAssetsConfig{Binding: "ASSETS"}); err != nil {
		t.Fatalf("Vite binding-only assets were rejected: %v", err)
	}
	if err := validateWranglerInputAssets(WorkerAssetsConfig{Binding: "PLATFORM"}); err == nil {
		t.Fatal("reserved generated assets binding was accepted")
	}
	if err := validateWranglerInputAssets(WorkerAssetsConfig{Directory: "../outside"}); err == nil {
		t.Fatal("unsafe declared assets directory was accepted")
	}
}

func TestParseWranglerRejectsBrokenConfig(t *testing.T) {
	if _, _, err := parseWranglerConfig(map[string]string{"wrangler.jsonc": `{"main": }`}); err == nil {
		t.Fatal("broken JSONC accepted")
	}
	if _, _, err := parseWranglerConfig(map[string]string{"wrangler.toml": "main = \n[[["}); err == nil {
		t.Fatal("broken TOML accepted")
	}
}
