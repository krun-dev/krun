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
	if cfg.Assets == nil || cfg.Assets.Directory != "./public" || cfg.Assets.NotFoundHandling != "single-page-application" || cfg.Assets.HTMLHandling != "auto-trailing-slash" {
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
	if cfg.Assets == nil || cfg.Assets.Directory != "./public" || cfg.Assets.NotFoundHandling != "404-page" || cfg.Assets.HTMLHandling != "none" {
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
	assets, entry, err := resolveWranglerBuild(cfg, nil, "")
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

func TestResolveWranglerBuildExplicitOverrideWins(t *testing.T) {
	cfg := &wranglerConfig{
		Main: "src/worker.ts",
		Assets: &wranglerAssets{
			Directory:        "./public",
			NotFoundHandling: "none",
			HTMLHandling:     "none",
			RunWorkerFirst:   []byte(`["/api/*"]`),
		},
	}
	override := &WorkerAssetsConfig{
		Directory:        "static",
		RunWorkerFirst:   []string{"/headers"},
		NotFoundHandling: "single-page-application",
		HTMLHandling:     "drop-trailing-slash",
	}
	assets, entry, err := resolveWranglerBuild(cfg, override, "custom.ts")
	if err != nil {
		t.Fatal(err)
	}
	if entry != "custom.ts" {
		t.Fatalf("explicit entrypoint must win: %q", entry)
	}
	if assets.Directory != "static" || assets.NotFoundHandling != "single-page-application" || assets.HTMLHandling != "drop-trailing-slash" {
		t.Fatalf("explicit assets must win: %+v", assets)
	}
	if len(assets.RunWorkerFirst) != 1 || assets.RunWorkerFirst[0] != "/headers" {
		t.Fatalf("explicit run_worker_first must win: %v", assets.RunWorkerFirst)
	}
}

func TestResolveWranglerBuildRejectsEscapes(t *testing.T) {
	for _, cfg := range []*wranglerConfig{
		{Main: "../outside.ts"},
		{Assets: &wranglerAssets{Directory: "../outside"}},
	} {
		if _, _, err := resolveWranglerBuild(cfg, nil, ""); err == nil {
			t.Fatalf("escape accepted: %+v", cfg)
		}
	}
}

func TestResolveWranglerBuildNormalisesPaths(t *testing.T) {
	cfg := &wranglerConfig{Main: "./src/index.ts", Assets: &wranglerAssets{Directory: "./public/"}}
	assets, entry, err := resolveWranglerBuild(cfg, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if entry != "src/index.ts" || assets.Directory != "public" {
		t.Fatalf("normalisation: %q %q", entry, assets.Directory)
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
