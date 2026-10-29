package control

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"testing"
)

func workerV2TestArchive(t *testing.T, manifest any, entries map[string][]byte) []byte {
	t.Helper()
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	all := map[string][]byte{"manifest.json": encoded}
	for name, data := range entries {
		all[name] = data
	}
	names := make([]string, 0, len(all))
	for name := range all {
		names = append(names, name)
	}
	sort.Strings(names)
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, name := range names {
		data := all[name]
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func workerV2TestManifest(main string, modules map[string]struct {
	kind string
	data []byte
}) (workerManifest, map[string][]byte) {
	manifest := workerManifest{
		Schema:             1,
		Profile:            workerProfile,
		CompatibilityDate:  workerDefaultCompatibilityDate,
		CompatibilityFlags: []string{"nodejs_compat"},
		MainModule:         main,
	}
	entries := make(map[string][]byte, len(modules))
	for name, module := range modules {
		modulePath := "modules/" + name
		manifest.Modules = append(manifest.Modules, workerManifestModule{
			Name: name, Path: modulePath, Type: module.kind,
			Digest: bytesDigest(module.data), Size: len(module.data),
		})
		entries[modulePath] = module.data
	}
	sort.Slice(manifest.Modules, func(i, j int) bool { return manifest.Modules[i].Name < manifest.Modules[j].Name })
	return manifest, entries
}

func TestParseWorkerV2Modules(t *testing.T) {
	manifest, entries := workerV2TestManifest("index.js", map[string]struct {
		kind string
		data []byte
	}{
		"index.js": {kind: "esm", data: []byte("export default {fetch(){return new Response('ok')}}")},
		// Wrangler rules, rather than extensions, determine these types.
		"legacy.js":      {kind: "commonjs", data: []byte("module.exports = {value: 1}")},
		"module.payload": {kind: "wasm", data: []byte{'\x00', 'a', 's', 'm', 1, 0, 0, 0}},
		"message.html":   {kind: "text", data: []byte("hello")},
		"payload.dat":    {kind: "data", data: []byte{0, 1, 2, 255}},
		"index.metadata": {kind: "source-map", data: []byte(`{"version":3,"sources":["index.ts"]}`)},
	})
	raw := workerV2TestArchive(t, manifest, entries)
	code, _, err := parseWorkerArchive(raw)
	if err != nil {
		t.Fatal(err)
	}
	if code.Profile != workerProfile || code.CompatibilityDate != workerDefaultCompatibilityDate || len(code.CompatibilityFlags) != 1 || code.MainModule != "index.js" || len(code.Modules) != 6 {
		t.Fatalf("metadata was not preserved: %+v", code)
	}
	if code.Modules["index.js"].Text == "" || !bytes.Equal(code.Modules["module.payload"].Bytes, entries["modules/module.payload"]) || !bytes.Equal(code.Modules["payload.dat"].Bytes, entries["modules/payload.dat"]) {
		t.Fatalf("module payloads were not preserved: %+v", code.Modules)
	}
	if code.Digest != bytesDigest(entries["modules/index.js"]) {
		t.Fatalf("main digest mismatch: %s", code.Digest)
	}
}

func TestParseWorkerV2RejectsInvalidTypedModulePayloads(t *testing.T) {
	tests := []struct {
		name string
		kind string
		data []byte
	}{
		{name: "wasm magic", kind: "wasm", data: []byte("not-wasm")},
		{name: "source map json", kind: "source-map", data: []byte("not-json")},
		{name: "text utf8", kind: "text", data: []byte{0xff}},
		{name: "commonjs utf8", kind: "commonjs", data: []byte{0xff}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manifest, entries := workerV2TestManifest("index.js", map[string]struct {
				kind string
				data []byte
			}{
				"index.js":  {kind: "esm", data: []byte("export default {fetch(){}}")},
				"typed.any": {kind: tt.kind, data: tt.data},
			})
			if _, _, err := parseWorkerArchive(workerV2TestArchive(t, manifest, entries)); err == nil {
				t.Fatal("invalid typed module payload was accepted")
			}
		})
	}
}

func TestParseWorkerV2RejectsPAXArchiveExtensions(t *testing.T) {
	manifest, entries := workerV2TestManifest("index.js", map[string]struct {
		kind string
		data []byte
	}{"index.js": {kind: "esm", data: []byte("export default {fetch(){}}")}})
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	entries["manifest.json"] = encoded
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, data := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg, Format: tar.FormatPAX, PAXRecords: map[string]string{"comment": "extended"}}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := parseWorkerArchive(buf.Bytes()); err == nil {
		t.Fatal("PAX artifact was accepted")
	}
}

func TestWorkerModuleNameRejectsControlCharacters(t *testing.T) {
	for _, name := range []string{"bad\x00name.js", "bad\nname.js", "bad\x7fname.js"} {
		if workerModuleNameValid(name) {
			t.Fatalf("unsafe module name %q was accepted", name)
		}
	}
}

func TestWorkerArtifactPathsAllowPortableNestedUSTARNames(t *testing.T) {
	nested := strings.Repeat("nested/", 20) + "module.js"
	if !workerModuleNameValid(nested) {
		t.Fatalf("portable nested module path was rejected: %q", nested)
	}
	if name, ok := workerAssetPath(strings.Repeat("assets/", 20) + "index.html"); !ok || name == "" {
		t.Fatal("portable nested asset path was rejected")
	}
	if workerModuleNameValid(strings.Repeat("x", 101) + ".js") {
		t.Fatal("module with an unsplittable ustar basename was accepted")
	}
	if _, ok := workerAssetPath("bad\nindex.html"); ok {
		t.Fatal("asset path with a control character was accepted")
	}

	manifest, entries := workerV2TestManifest(nested, map[string]struct {
		kind string
		data []byte
	}{nested: {kind: "esm", data: []byte("export default {fetch(){}}")}})
	code, _, err := parseWorkerArchive(workerV2TestArchive(t, manifest, entries))
	if err != nil {
		t.Fatal(err)
	}
	if code.MainModule != nested {
		t.Fatalf("nested main module was not preserved: %q", code.MainModule)
	}
}

func TestParseWorkerV2AllowsManyAdditionalModules(t *testing.T) {
	modules := map[string]struct {
		kind string
		data []byte
	}{"index.js": {kind: "esm", data: []byte("export default {fetch(){}}")}}
	for i := 0; i < 64; i++ {
		name := fmt.Sprintf("lang/%02d.mjs", i)
		modules[name] = struct {
			kind string
			data []byte
		}{kind: "esm", data: []byte("export default " + fmt.Sprint(i))}
	}
	manifest, entries := workerV2TestManifest("index.js", modules)
	code, _, err := parseWorkerArchive(workerV2TestArchive(t, manifest, entries))
	if err != nil {
		t.Fatal(err)
	}
	if len(code.Modules) != 65 {
		t.Fatalf("expected 65 modules, got %d", len(code.Modules))
	}
}

func TestParseWorkerV2AllowsIncompressibleModulesAtExpandedLimit(t *testing.T) {
	main := []byte("export default {fetch(){return new Response('ok')}}")
	modules := map[string]struct {
		kind string
		data []byte
	}{"index.js": {kind: "esm", data: main}}
	remaining := workerModulesTotalMax - len(main)
	random := rand.New(rand.NewSource(1))
	for i := 0; remaining > 0; i++ {
		size := workerModuleMax
		if remaining < size {
			size = remaining
		}
		data := make([]byte, size)
		if _, err := random.Read(data); err != nil {
			t.Fatal(err)
		}
		modules[fmt.Sprintf("payload/%d.bin", i)] = struct {
			kind string
			data []byte
		}{kind: "data", data: data}
		remaining -= size
	}
	manifest, entries := workerV2TestManifest("index.js", modules)
	raw := workerV2TestArchive(t, manifest, entries)
	if len(raw) <= 8<<20 {
		t.Fatalf("fixture did not exercise compressed envelope headroom: %d", len(raw))
	}
	if _, _, err := parseWorkerArchive(raw); err != nil {
		t.Fatal(err)
	}
}

func TestParseWorkerV2RejectsInvalidMetadataAndModules(t *testing.T) {
	base, entries := workerV2TestManifest("index.js", map[string]struct {
		kind string
		data []byte
	}{"index.js": {kind: "esm", data: []byte("export default {fetch(){}}")}})

	tests := []struct {
		name   string
		mutate func(*workerManifest, map[string][]byte)
	}{
		{name: "future compatibility date", mutate: func(m *workerManifest, _ map[string][]byte) { m.CompatibilityDate = "2026-09-29" }},
		{name: "duplicate compatibility flag", mutate: func(m *workerManifest, _ map[string][]byte) {
			m.CompatibilityFlags = []string{"nodejs_compat", "nodejs_compat"}
		}},
		{name: "invalid compatibility flag", mutate: func(m *workerManifest, _ map[string][]byte) { m.CompatibilityFlags = []string{"NODE compat"} }},
		{name: "old profile", mutate: func(m *workerManifest, _ map[string][]byte) { m.Profile = "esbuild-esm-v1" }},
		{name: "undeclared main", mutate: func(m *workerManifest, _ map[string][]byte) { m.MainModule = "missing.js" }},
		{name: "invalid module path", mutate: func(m *workerManifest, _ map[string][]byte) { m.Modules[0].Path = "modules/../index.js" }},
		{name: "digest mismatch", mutate: func(m *workerManifest, _ map[string][]byte) { m.Modules[0].Digest = bytesDigest([]byte("tampered")) }},
		{name: "undeclared archive entry", mutate: func(_ *workerManifest, files map[string][]byte) {
			files["modules/extra.js"] = []byte("export default 1")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manifest := base
			manifest.Modules = append([]workerManifestModule(nil), base.Modules...)
			manifest.CompatibilityFlags = append([]string(nil), base.CompatibilityFlags...)
			files := make(map[string][]byte, len(entries)+1)
			for name, data := range entries {
				files[name] = append([]byte(nil), data...)
			}
			tt.mutate(&manifest, files)
			if _, _, err := parseWorkerArchive(workerV2TestArchive(t, manifest, files)); err == nil {
				t.Fatal("invalid artifact was accepted")
			}
		})
	}
}

func TestParseWorkerV2RejectsUnknownManifestFields(t *testing.T) {
	code := []byte("export default {fetch(){}}")
	manifest := map[string]any{
		"schema_version":     1,
		"profile":            workerProfile,
		"compatibility_date": workerDefaultCompatibilityDate,
		"main_module":        "index.js",
		"modules": []map[string]any{{
			"name": "index.js", "path": "modules/index.js", "type": "esm",
			"sha256": bytesDigest(code), "size_bytes": len(code),
		}},
		"unexpected": true,
	}
	if _, _, err := parseWorkerArchive(workerV2TestArchive(t, manifest, map[string][]byte{"modules/index.js": code})); err == nil {
		t.Fatal("unknown manifest field was accepted")
	}
}

func TestWorkerAssetsRejectPrototypeBindingNames(t *testing.T) {
	for _, binding := range []string{"__proto__", "prototype", "constructor"} {
		t.Run(binding, func(t *testing.T) {
			if err := validateWorkerAssets(&WorkerAssetsConfig{Directory: "public", Binding: binding}); err == nil {
				t.Fatalf("prototype-sensitive binding %q was accepted", binding)
			}
		})
	}
}
