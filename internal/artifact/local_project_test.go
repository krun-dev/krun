package artifact_test

import (
	"archive/zip"
	"bytes"
	"errors"
	"io/fs"
	"path"
	"strings"
	"testing"

	"github.com/krun-dev/krun/internal/artifact"
	"github.com/krun-dev/krun/internal/domain"
)

func TestPrepareLocalProjectStaticPreservesAssets(t *testing.T) {
	binary := []byte{0, 1, 0xfe, 0xff}
	project, err := artifact.PrepareLocalProject(nil, map[string][]byte{
		"index.html":      []byte("<title>Ready</title>"),
		"assets/logo.png": binary,
		"_headers":        []byte("/*\n X-Test: yes"),
		"_redirects":      []byte("/old /new 301"),
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if project.Kind != "static" || project.ConfigPath != "wrangler.jsonc" || project.Entrypoint != "" ||
		project.CompatibilityDate != artifact.WorkerDefaultCompatibilityDate {
		t.Fatalf("unexpected project metadata: %#v", project)
	}
	config, _, err := artifact.ParseWranglerConfig(project.Files)
	if err != nil {
		t.Fatal(err)
	}
	if config == nil || config.Assets == nil || config.Assets.Directory == "" {
		t.Fatalf("expected generated Wrangler assets configuration, got %#v", config)
	}
	if len(project.Files) != 5 || project.Files[path.Join(config.Assets.Directory, "index.html")] != "<title>Ready</title>" ||
		!bytes.Equal([]byte(project.Files[path.Join(config.Assets.Directory, "assets/logo.png")]), binary) ||
		project.Files[path.Join(config.Assets.Directory, "_headers")] != "/*\n X-Test: yes" ||
		project.Files[path.Join(config.Assets.Directory, "_redirects")] != "/old /new 301" {
		t.Fatalf("static files were not staged as assets: %#v", project.Files)
	}
}

func TestPrepareLocalProjectWranglerRootTakesPriority(t *testing.T) {
	project, err := artifact.PrepareLocalProject(nil, map[string][]byte{
		"wrangler.jsonc":                []byte(`{"main":"./src/worker.ts","compatibility_date":"2024-10-01","compatibility_flags":["nodejs_compat"]}`),
		"wrangler.toml":                 []byte("ignored"),
		"src/worker.ts":                 []byte("export default {}"),
		"index.html":                    []byte("<title>Client</title>"),
		"nested/wrangler.jsonc":         []byte(`{"assets":{"directory":"public"}}`),
		".wrangler/deploy/config.json":  []byte("{}"),
		"node_modules/package/index.js": []byte("excluded"),
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if project.Kind != "worker" || project.ConfigPath != "wrangler.jsonc" || project.Entrypoint != "src/worker.ts" ||
		project.CompatibilityDate != "2024-10-01" || len(project.CompatibilityFlags) != 1 || project.CompatibilityFlags[0] != "nodejs_compat" {
		t.Fatalf("unexpected project metadata: %#v", project)
	}
	if len(project.Files) != 5 || project.Files["src/worker.ts"] != "export default {}" {
		t.Fatalf("unexpected project files: %#v", project.Files)
	}
}

func TestPrepareLocalProjectUnwrapsZIPAndKeepsAssetsOnlyWrangler(t *testing.T) {
	archive := localProjectZIP(t,
		localZIPEntry{name: "project/", mode: fs.ModeDir | 0o755},
		localZIPEntry{name: "project/wrangler.toml", body: []byte("[assets]\ndirectory='public'")},
		localZIPEntry{name: "project/public/index.html", body: []byte("<title>Ready</title>")},
	)
	project, err := artifact.PrepareLocalProject(archive, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if project.Kind != "worker" || project.ConfigPath != "wrangler.toml" || project.Entrypoint != "" ||
		project.Files["public/index.html"] != "<title>Ready</title>" {
		t.Fatalf("unexpected project: %#v", project)
	}
}

func TestPrepareLocalProjectUnwrapsLocalizedZIPFolder(t *testing.T) {
	archive := localProjectZIP(t,
		localZIPEntry{name: "我的站点/index.html", body: []byte("<title>Ready</title>")},
		localZIPEntry{name: "我的站点/site logo.svg", body: []byte("<svg/>")},
	)
	project, err := artifact.PrepareLocalProject(archive, nil, "")
	if err != nil || project.Kind != "static" || project.Files["public/site logo.svg"] != "<svg/>" {
		t.Fatalf("ZIP wrapper or space in asset name was not preserved: %+v, %v", project, err)
	}
}

func TestPrepareLocalProjectFindsUniqueNestedWrangler(t *testing.T) {
	project, err := artifact.PrepareLocalProject(nil, map[string][]byte{
		"README.md":                 []byte("# Monorepo"),
		"package.json":              []byte(`{"workspaces":["apps/*"]}`),
		"apps/worker/wrangler.json": []byte(`{"main":"generated/worker.js"}`),
		"apps/worker/package.json":  []byte(`{"scripts":{"build":"node build.mjs"}}`),
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if project.ConfigPath != "wrangler.json" || project.Entrypoint != "generated/worker.js" || len(project.Files) != 2 {
		t.Fatalf("unexpected project: %#v", project)
	}
}

func TestPrepareLocalProjectRequiresRootForAmbiguousWrangler(t *testing.T) {
	files := map[string][]byte{
		"repo/apps/a/wrangler.json": []byte(`{"main":"a.js"}`),
		"repo/apps/a/a.js":          []byte("export default {}"),
		"repo/apps/b/wrangler.json": []byte(`{"main":"b.js"}`),
		"repo/apps/b/b.js":          []byte("export default {}"),
	}
	_, err := artifact.PrepareLocalProject(nil, files, "")
	var problem *domain.APIError
	if !errors.As(err, &problem) || problem.Code != "LOCAL_PROJECT_ROOT_REQUIRED" ||
		!strings.Contains(problem.Message, "repo/apps/a") || !strings.Contains(problem.Message, "repo/apps/b") {
		t.Fatalf("expected root selection with candidate paths, got %v", err)
	}

	project, err := artifact.PrepareLocalProject(nil, files, "repo/apps/b")
	if err != nil {
		t.Fatal(err)
	}
	if project.Entrypoint != "b.js" || len(project.Files) != 2 || project.Files["b.js"] != "export default {}" {
		t.Fatalf("unexpected project: %#v", project)
	}

	if _, err := artifact.PrepareLocalProject(nil, files, "apps/b"); err == nil {
		t.Fatal("expected exact root matching against uploaded paths")
	}
}

func TestPrepareLocalProjectRequiresRootForMixedProjects(t *testing.T) {
	files := map[string][]byte{
		"repo/docs/index.html":       []byte("<title>Docs</title>"),
		"repo/docs/about/index.html": []byte("<title>About</title>"),
		"repo/app/wrangler.json":     []byte(`{"assets":{"directory":"public"}}`),
		"repo/app/public/index.html": []byte("<title>App</title>"),
	}
	_, err := artifact.PrepareLocalProject(nil, files, "")
	var problem *domain.APIError
	if !errors.As(err, &problem) || problem.Code != "LOCAL_PROJECT_ROOT_REQUIRED" ||
		!strings.Contains(problem.Message, "repo/docs") || !strings.Contains(problem.Message, "repo/app") ||
		strings.Contains(problem.Message, "repo/docs/about") || strings.Contains(problem.Message, "repo/app/public") {
		t.Fatalf("expected only the two project roots, got %v", err)
	}
	project, err := artifact.PrepareLocalProject(nil, files, "repo/docs")
	if err != nil || project.Kind != "static" || len(project.Files) != 3 {
		t.Fatalf("static root override failed: %+v, %v", project, err)
	}
}

func TestPrepareLocalProjectRejectsUnbuiltStaticSource(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files map[string][]byte
	}{
		{
			name: "build manifest",
			files: map[string][]byte{
				"index.html":   []byte("<title>Source</title>"),
				"package.json": []byte(`{"scripts":{"build":"vite build"}}`),
			},
		},
		{
			name: "Vite client reference",
			files: map[string][]byte{
				"index.html": []byte(`<script type="module" src="/@vite/client"></script>`),
			},
		},
		{
			name: "source module",
			files: map[string][]byte{
				"index.html":     []byte(`<script type="module" src="/src/client.tsx"></script>`),
				"src/client.tsx": []byte("export default <h1>Source</h1>"),
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := artifact.PrepareLocalProject(nil, tc.files, "")
			var problem *domain.APIError
			if !errors.As(err, &problem) || !strings.Contains(problem.Message, "build output") {
				t.Fatalf("expected guidance to upload build output, got %v", err)
			}
		})
	}
}

func TestPrepareLocalProjectRejectsUnsafeZIP(t *testing.T) {
	tests := []struct {
		name    string
		entries []localZIPEntry
	}{
		{name: "traversal", entries: []localZIPEntry{{name: "site/../index.html"}}},
		{name: "Windows path", entries: []localZIPEntry{{name: "C:/index.html"}}},
		{name: "duplicate", entries: []localZIPEntry{{name: "index.html"}, {name: "index.html"}}},
		{name: "symlink", entries: []localZIPEntry{{name: "public/link", mode: fs.ModeSymlink | 0o777}}},
		{name: "special file", entries: []localZIPEntry{{name: "site/pipe", mode: fs.ModeNamedPipe | 0o644}}},
		{name: "file is an ancestor", entries: []localZIPEntry{{name: "index.html"}, {name: "index.html/child"}}},
		{name: "oversized expanded file", entries: []localZIPEntry{{name: "index.html", body: bytes.Repeat([]byte{0}, artifact.WorkerRepositoryProjectFileMax+1)}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			archive := localProjectZIP(t, tc.entries...)
			if _, err := artifact.PrepareLocalProject(archive, nil, ""); err == nil {
				t.Fatal("expected invalid archive")
			}
		})
	}
	if _, err := artifact.PrepareLocalProject([]byte("invalid ZIP"), nil, ""); err == nil {
		t.Fatal("expected invalid ZIP")
	}
}

func TestPrepareLocalProjectRejectsLocalCredentials(t *testing.T) {
	_, err := artifact.PrepareLocalProject(nil, map[string][]byte{
		"index.html":      []byte("<title>Ready</title>"),
		".env.production": []byte("EXAMPLE_KEY=replace-me"),
	}, "")
	var problem *domain.APIError
	if !errors.As(err, &problem) || !strings.Contains(problem.Message, ".env.production") {
		t.Fatalf("expected local environment file rejection, got %v", err)
	}
}

func TestPrepareLocalProjectValidatesStaticAssetLimitsAndRules(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files map[string][]byte
	}{
		{name: "file size", files: map[string][]byte{"large.bin": make([]byte, artifact.WorkerAssetMax+1)}},
		{name: "malformed rules", files: map[string][]byte{"_headers": []byte{0xff}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.files["index.html"] = []byte("<title>Ready</title>")
			if _, err := artifact.PrepareLocalProject(nil, tc.files, ""); err == nil {
				t.Fatal("expected invalid static assets")
			}
		})
	}
}

func TestPrepareLocalProjectRequiresOneInput(t *testing.T) {
	files := map[string][]byte{"index.html": []byte("<title>Ready</title>")}
	archive := localProjectZIP(t, localZIPEntry{name: "index.html", body: files["index.html"]})
	if _, err := artifact.PrepareLocalProject(archive, files, ""); err == nil {
		t.Fatal("expected ZIP and file map to be mutually exclusive")
	}
	if _, err := artifact.PrepareLocalProject(nil, nil, ""); err == nil {
		t.Fatal("expected ZIP or file map to be required")
	}
}

type localZIPEntry struct {
	name string
	body []byte
	mode fs.FileMode
}

func localProjectZIP(t *testing.T, entries ...localZIPEntry) []byte {
	t.Helper()
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.name, Method: zip.Deflate}
		if entry.mode != 0 {
			header.SetMode(entry.mode)
		}
		file, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(entry.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return archive.Bytes()
}
