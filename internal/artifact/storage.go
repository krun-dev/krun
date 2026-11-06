package artifact

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"

	domain "github.com/krun-dev/krun/internal/domain"
)

// Artifact modules may total 8 MiB. Incompressible payload plus manifest and
// tar/gzip framing is slightly larger, so the compressed envelope needs its
// own headroom while the expanded module limit remains exact below.
const WorkerArchiveMax = 9 << 20

const WorkerArchiveAssetsMax = 32 << 20

const WorkerModuleMax = 2 << 20

const WorkerModulesTotalMax = 8 << 20

const WorkerModulesMax = 512

// The maximum declared 512 modules + 500 assets with portable nested paths
// can exceed 256 KiB even though all payload limits remain bounded.
const WorkerManifestMax = 1 << 20

const WorkerAssetMax = 5 << 20

const WorkerAssetsTotalMax = 20 << 20

const WorkerAssetsFilesMax = 500

const WorkerSourceMax = 32 << 20

const WorkerProfile = "workerd-bundle-v2"

type WorkerManifest struct {
	Schema             int                    `json:"schema_version"`
	Profile            string                 `json:"profile"`
	CompatibilityDate  string                 `json:"compatibility_date"`
	CompatibilityFlags []string               `json:"compatibility_flags,omitempty"`
	MainModule         string                 `json:"main_module"`
	Modules            []WorkerManifestModule `json:"modules"`
	Assets             *WorkerManifestAssets  `json:"assets,omitempty"`
}

type WorkerManifestModule struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Type   string `json:"type"`
	Digest string `json:"sha256"`
	Size   int    `json:"size_bytes"`
}

type WorkerManifestAssets struct {
	BasePath  string `json:"base_path,omitempty"`
	Headers   string `json:"_headers,omitempty"`
	Redirects string `json:"_redirects,omitempty"`

	Directory        string               `json:"directory"`
	Binding          string               `json:"binding,omitempty"`
	RunWorkerFirst   []string             `json:"run_worker_first"`
	NotFoundHandling string               `json:"not_found_handling"`
	HTMLHandling     string               `json:"html_handling"`
	Files            []WorkerManifestFile `json:"files"`
}

type WorkerManifestFile struct {
	Path        string `json:"path"`
	Digest      string `json:"sha256"`
	Size        int    `json:"size_bytes"`
	ContentType string `json:"content_type"`
}

type WorkerAsset struct {
	Digest      string
	Size        int
	ContentType string
}

type WorkerModule struct {
	Type   string
	Digest string
	Size   int
	Text   string
	Bytes  []byte
}

type WorkerCode struct {
	ManifestRaw        []byte                     `json:"-"`
	Profile            string                     `json:"profile"`
	Digest             string                     `json:"digest"`
	ArchiveDigest      string                     `json:"archive_digest"`
	Size               int                        `json:"size"`
	CompatibilityDate  string                     `json:"compatibilityDate"`
	CompatibilityFlags []string                   `json:"compatibilityFlags,omitempty"`
	MainModule         string                     `json:"mainModule"`
	Modules            map[string]WorkerModule    `json:"modules"`
	Assets             *domain.WorkerAssetsConfig `json:"assets,omitempty"`
	AssetFiles         map[string]WorkerAsset     `json:"asset_files,omitempty"`
	AssetBytes         map[string][]byte          `json:"-"`
}

// Known extensions get deterministic browser-facing types. Unknown and
// extensionless assets remain valid Cloudflare static assets and are served as
// opaque bytes instead of being content-sniffed into an executable type.
var WorkerAssetTypes = map[string]string{
	".html": "text/html; charset=utf-8", ".htm": "text/html; charset=utf-8",
	".js": "application/javascript; charset=utf-8", ".mjs": "application/javascript; charset=utf-8",
	".css": "text/css; charset=utf-8", ".json": "application/json", ".map": "application/json",
	".txt": "text/plain; charset=utf-8", ".csv": "text/csv; charset=utf-8", ".md": "text/markdown; charset=utf-8",
	".svg": "image/svg+xml", ".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg",
	".gif": "image/gif", ".webp": "image/webp", ".avif": "image/avif", ".ico": "image/x-icon",
	".woff": "font/woff", ".woff2": "font/woff2", ".ttf": "font/ttf", ".otf": "font/otf",
	".webmanifest": "application/manifest+json", ".xml": "application/xml", ".pdf": "application/pdf",
	".wasm": "application/wasm", ".glb": "model/gltf-binary", ".gltf": "model/gltf+json",
	".mp3": "audio/mpeg", ".mp4": "video/mp4", ".webm": "video/webm", ".wav": "audio/wav", ".ogg": "audio/ogg",
}

var WorkerAssetBindingName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

// Keep in sync with reservedBindings and magicBindings in workers/runtime/entry.js.
var WorkerReservedBindings = map[string]bool{
	"PLATFORM": true, "LOADER": true, "RUNTIME_TOKEN": true, "RUNTIME_POOL": true,
	"MANAGEMENT_TOKEN": true, "CONFIG_BASE": true, "POD_NAME": true, "POD_UID": true,
	"ASSETS_CACHE_MAX":   true,
	"STORAGE_ACCESS_KEY": true, "STORAGE_SECRET_KEY": true, "STORAGE_REGION": true,
	"STATIC_DOMAINS": true,
	// These names have special prototype semantics on ordinary JavaScript
	// objects. Reject them even though the runtime also uses null-prototype envs.
	"__proto__": true, "prototype": true, "constructor": true,
}

func WorkerAssetType(name string) (string, bool) {
	kind, ok := WorkerAssetTypes[strings.ToLower(path.Ext(name))]
	if !ok {
		return "application/octet-stream", true
	}
	return kind, true
}

// Both query handling modes and pattern syntax are validated at submit time.
func ValidateWorkerAssets(c *domain.WorkerAssetsConfig) error {
	if c == nil {
		return nil
	}
	if c.Directory == "" || path.IsAbs(c.Directory) || path.Clean(c.Directory) != c.Directory || c.Directory == "." || strings.HasPrefix(c.Directory, "../") {
		return domain.Problem(422, "WORKER_ASSETS_INVALID", "assets.directory must be a safe relative directory")
	}
	if c.Binding != "" && (!WorkerAssetBindingName.MatchString(c.Binding) || strings.HasPrefix(c.Binding, "KRUN_") || WorkerReservedBindings[c.Binding]) {
		return domain.Problem(422, "WORKER_ASSETS_INVALID", "assets.binding must be a non-reserved Worker binding name")
	}
	if len(c.RunWorkerFirst) > 16 {
		return domain.Problem(422, "WORKER_ASSETS_INVALID", "At most 16 run_worker_first patterns are supported")
	}
	seen := map[string]bool{}
	for _, p := range c.RunWorkerFirst {
		raw := strings.TrimPrefix(p, "!")
		if raw == "" || len(p) > 128 || !strings.HasPrefix(raw, "/") || strings.Contains(raw, "..") || strings.Contains(raw, "//") {
			return domain.Problem(422, "WORKER_ASSETS_INVALID", "Invalid run_worker_first pattern: "+p)
		}
		stars := strings.Count(raw, "*")
		if stars > 1 || stars == 1 && !strings.HasSuffix(raw, "*") {
			return domain.Problem(422, "WORKER_ASSETS_INVALID", "run_worker_first only supports a trailing *: "+p)
		}
		if seen[p] {
			return domain.Problem(422, "WORKER_ASSETS_INVALID", "Duplicate run_worker_first pattern: "+p)
		}
		seen[p] = true
	}
	if c.NotFoundHandling != "" && c.NotFoundHandling != "none" && c.NotFoundHandling != "single-page-application" && c.NotFoundHandling != "404-page" {
		return domain.Problem(422, "WORKER_ASSETS_INVALID", "not_found_handling must be none, single-page-application or 404-page")
	}
	if c.HTMLHandling != "" && c.HTMLHandling != "none" && c.HTMLHandling != "auto-trailing-slash" && c.HTMLHandling != "force-trailing-slash" && c.HTMLHandling != "drop-trailing-slash" {
		return domain.Problem(422, "WORKER_ASSETS_INVALID", "html_handling must be none, auto-trailing-slash, force-trailing-slash or drop-trailing-slash")
	}
	return ValidateAssetRules(c)
}

func WorkerAssetPath(name string) (string, bool) {
	if name == "" || len(name) > 240 || strings.HasPrefix(name, "/") || path.Clean(name) != name || strings.HasPrefix(name, "../") || strings.Contains(name, "\\") ||
		strings.IndexFunc(name, func(r rune) bool { return r < 0x20 || r == 0x7f }) != -1 || !WorkerUSTARPathValid("assets/"+name) {
		return "", false
	}
	return name, true
}

func BytesDigest(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }

func WorkerModuleNameValid(name string) bool {
	return name != "" && len(name) <= 240 && !path.IsAbs(name) && path.Clean(name) == name &&
		!strings.HasPrefix(name, "../") && !strings.Contains(name, "\\") &&
		strings.IndexFunc(name, func(r rune) bool { return r < 0x20 || r == 0x7f }) == -1 &&
		WorkerUSTARPathValid("modules/"+name)
}

// workerUSTARPathValid accepts either the short name field or the portable
// prefix/name split used by the builder.  This keeps deeply nested framework
// output without silently falling back to GNU or PAX tar extensions.
func WorkerUSTARPathValid(name string) bool {
	if len(name) <= 100 {
		return true
	}
	if len(name) > 256 {
		return false
	}
	for i := strings.LastIndexByte(name, '/'); i >= 0; i = strings.LastIndexByte(name[:i], '/') {
		if i <= 155 && len(name)-i-1 <= 100 {
			return true
		}
	}
	return false
}

// Wrangler rules determine module type independently of the filename. For
// example, a project may load `template.html` as Text, `dictionary.dat` as
// Data, or explicitly treat a `.js` file as CommonJS. The manifest type is
// therefore authoritative; payload validation below protects the runtime.
func WorkerModuleTypeValid(kind string) bool {
	switch kind {
	case "esm", "commonjs", "wasm", "text", "data", "source-map":
		return true
	default:
		return false
	}
}

// Parse the current artifact profile, with an optional assets/ tree.
// Paths, duplicates, links, sizes and all digests are checked before Loader sees code.
func ParseWorkerArchive(raw []byte) (WorkerCode, string, error) {
	var out WorkerCode
	max := WorkerArchiveMax
	if len(raw) == 0 || len(raw) > WorkerArchiveAssetsMax {
		return out, "", errors.New("artifact size exceeds profile")
	}
	gz, e := gzip.NewReader(bytes.NewReader(raw))
	if e != nil {
		return out, "", errors.New("invalid gzip archive")
	}
	defer gz.Close()
	tr := tar.NewReader(io.LimitReader(gz, int64(WorkerArchiveAssetsMax)+1))
	files := map[string][]byte{}
	total := 0
	for {
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return out, "", errors.New("invalid tar archive")
		}
		_, duplicate := files[h.Name]
		if h.Format != tar.FormatUSTAR || h.Typeflag != tar.TypeReg || h.Size < 0 || duplicate || h.Name == "" || path.Clean(h.Name) != h.Name || path.IsAbs(h.Name) || strings.HasPrefix(h.Name, "../") || strings.Contains(h.Name, "\\") {
			return out, "", errors.New("unsupported artifact entry")
		}
		limit := int64(WorkerModuleMax)
		allowed := strings.HasPrefix(h.Name, "modules/")
		if h.Name == "manifest.json" {
			allowed, limit = true, WorkerManifestMax
		} else if strings.HasPrefix(h.Name, "assets/") {
			allowed, limit = true, WorkerAssetMax
		}
		if !allowed || h.Size > limit {
			return out, "", errors.New("unsupported artifact entry")
		}
		data, e := io.ReadAll(io.LimitReader(tr, limit+1))
		if e != nil || len(data) != int(h.Size) {
			return out, "", errors.New("invalid artifact file size")
		}
		total += len(data)
		if total > WorkerArchiveAssetsMax {
			return out, "", errors.New("expanded artifact too large")
		}
		files[h.Name] = data
	}
	// Consume gzip trailer too; a tar EOF is not sufficient integrity verification.
	tail, e := io.ReadAll(io.LimitReader(gz, 1025))
	if e != nil || len(tail) > 1024 {
		return out, "", errors.New("invalid gzip trailer or trailing payload")
	}
	if len(files) < 2 || files["manifest.json"] == nil {
		return out, "", errors.New("manifest and module required")
	}

	var m WorkerManifest
	decoder := json.NewDecoder(bytes.NewReader(files["manifest.json"]))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&m) != nil || decoder.Decode(&struct{}{}) != io.EOF || m.Schema != 2 || m.Profile != WorkerProfile || !WorkerModuleNameValid(m.MainModule) || len(m.Modules) == 0 || len(m.Modules) > WorkerModulesMax {
		return out, "", errors.New("unsupported manifest")
	}
	compatibilityDate, compatibilityFlags, compatibilityErr := NormalizeWorkerCompatibility(m.CompatibilityDate, m.CompatibilityFlags)
	if compatibilityErr != nil || compatibilityDate != m.CompatibilityDate {
		return out, "", errors.New("unsupported manifest compatibility")
	}
	m.CompatibilityFlags = compatibilityFlags
	moduleNames := make(map[string]bool, len(m.Modules))
	modulePaths := make(map[string]bool, len(m.Modules))
	modules := make(map[string]WorkerModule, len(m.Modules))
	modulesTotal := 0
	mainDigest := ""
	for _, module := range m.Modules {
		if !WorkerModuleNameValid(module.Name) || !WorkerModuleTypeValid(module.Type) || module.Path != "modules/"+module.Name || moduleNames[module.Name] || modulePaths[module.Path] {
			return out, "", errors.New("invalid or duplicate module")
		}
		moduleNames[module.Name], modulePaths[module.Path] = true, true
		data, ok := files[module.Path]
		if !ok {
			return out, "", errors.New("module file missing from archive: " + module.Name)
		}
		if module.Size != len(data) || module.Digest != BytesDigest(data) || len(data) > WorkerModuleMax {
			return out, "", errors.New("module digest mismatch: " + module.Name)
		}
		modulesTotal += len(data)
		if modulesTotal > WorkerModulesTotalMax {
			return out, "", errors.New("modules exceed total size limit")
		}
		value := WorkerModule{Type: module.Type, Digest: module.Digest, Size: module.Size}
		switch module.Type {
		case "esm", "commonjs", "text":
			if !utf8.Valid(data) {
				return out, "", errors.New("module is not valid UTF-8: " + module.Name)
			}
			value.Text = string(data)
		case "source-map":
			if !utf8.Valid(data) || !json.Valid(data) {
				return out, "", errors.New("source map is not valid JSON: " + module.Name)
			}
			value.Text = string(data)
		case "wasm":
			if len(data) < 8 || !bytes.Equal(data[:4], []byte{'\x00', 'a', 's', 'm'}) {
				return out, "", errors.New("module is not valid WebAssembly: " + module.Name)
			}
			value.Bytes = data
		case "data":
			value.Bytes = data
		}
		modules[module.Name] = value
		if module.Name == m.MainModule {
			if module.Type != "esm" && module.Type != "commonjs" {
				return out, "", errors.New("main module must be JavaScript")
			}
			mainDigest = module.Digest
		}
	}
	if mainDigest == "" {
		return out, "", errors.New("main module is not declared")
	}
	out = WorkerCode{ManifestRaw: append([]byte(nil), files["manifest.json"]...), Profile: m.Profile, Digest: mainDigest, ArchiveDigest: BytesDigest(raw), Size: modulesTotal, CompatibilityDate: m.CompatibilityDate, CompatibilityFlags: append([]string(nil), m.CompatibilityFlags...), MainModule: m.MainModule, Modules: modules}
	if m.Assets == nil {
		if len(files) != len(m.Modules)+1 || len(raw) > max {
			return out, "", errors.New("artifact has unexpected entries")
		}
		return out, BytesDigest(files["manifest.json"]), nil
	}
	if e := ValidateWorkerAssets(&domain.WorkerAssetsConfig{Directory: m.Assets.Directory, BasePath: m.Assets.BasePath, Headers: m.Assets.Headers, Redirects: m.Assets.Redirects, Binding: m.Assets.Binding, RunWorkerFirst: m.Assets.RunWorkerFirst, NotFoundHandling: m.Assets.NotFoundHandling, HTMLHandling: m.Assets.HTMLHandling}); e != nil {
		return out, "", e
	}
	if len(m.Assets.Files) > WorkerAssetsFilesMax {
		return out, "", errors.New("too many asset files")
	}
	seen := map[string]bool{}
	totalAssets := 0
	assetFiles := map[string]WorkerAsset{}
	assetBytes := map[string][]byte{}
	for _, f := range m.Assets.Files {
		name, ok := WorkerAssetPath(f.Path)
		if !ok || seen[name] || name == "_headers" || name == "_redirects" {
			return out, "", errors.New("invalid or duplicate asset path")
		}
		seen[name] = true
		data, ok := files["assets/"+name]
		if !ok {
			return out, "", errors.New("asset file missing from archive: " + name)
		}
		kind, ok := WorkerAssetType(name)
		if !ok || kind != f.ContentType {
			return out, "", errors.New("unsupported asset type: " + name)
		}
		if f.Size != len(data) || f.Digest != BytesDigest(data) || len(data) > WorkerAssetMax {
			return out, "", errors.New("asset digest mismatch: " + name)
		}
		totalAssets += len(data)
		if totalAssets > WorkerAssetsTotalMax {
			return out, "", errors.New("assets exceed total size limit")
		}
		assetFiles[name] = WorkerAsset{Digest: f.Digest, Size: f.Size, ContentType: f.ContentType}
		assetBytes[name] = data
	}
	if len(files) != len(m.Assets.Files)+len(m.Modules)+1 {
		return out, "", errors.New("archive contains undeclared entries")
	}
	if m.Assets.NotFoundHandling == "single-page-application" && !seen["index.html"] {
		return out, "", errors.New("single-page-application requires index.html")
	}
	out.Assets = &domain.WorkerAssetsConfig{Directory: m.Assets.Directory, BasePath: m.Assets.BasePath, Headers: m.Assets.Headers, Redirects: m.Assets.Redirects, Binding: m.Assets.Binding, RunWorkerFirst: m.Assets.RunWorkerFirst, NotFoundHandling: m.Assets.NotFoundHandling, HTMLHandling: m.Assets.HTMLHandling}
	out.AssetFiles = assetFiles
	out.AssetBytes = assetBytes
	return out, BytesDigest(files["manifest.json"]), nil
}

var WorkerHashedAsset = regexp.MustCompile(`[-.][0-9a-f]{8,}\.[a-z0-9]+$`)

func SafeWorkerLog(text string) string {
	if len(text) > 32768 {
		text = text[:32768]
	} // Never persist signed upload URLs in API logs.
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, "X-Amz-") {
			return "Builder log contained a signed URL and was redacted"
		}
	}
	return text
}
