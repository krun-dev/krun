package control

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

const workerArchiveMax = 8 << 20
const workerArchiveAssetsMax = 32 << 20
const workerModuleMax = 2 << 20
const workerAssetMax = 5 << 20
const workerAssetsTotalMax = 20 << 20
const workerAssetsFilesMax = 500
const workerSourceMax = 32 << 20
const workerProfile = "esbuild-esm-v1"

type workerManifest struct {
	Schema     int                    `json:"schema_version"`
	Profile    string                 `json:"profile"`
	MainModule string                 `json:"main_module"`
	Modules    []workerManifestModule `json:"modules"`
	Assets     *workerManifestAssets  `json:"assets,omitempty"`
}
type workerManifestModule struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Type   string `json:"type"`
	Digest string `json:"sha256"`
	Size   int    `json:"size_bytes"`
}
type workerManifestAssets struct {
	Directory        string               `json:"directory"`
	RunWorkerFirst   []string             `json:"run_worker_first"`
	NotFoundHandling string               `json:"not_found_handling"`
	HTMLHandling     string               `json:"html_handling"`
	Files            []workerManifestFile `json:"files"`
}
type workerManifestFile struct {
	Path        string `json:"path"`
	Digest      string `json:"sha256"`
	Size        int    `json:"size_bytes"`
	ContentType string `json:"content_type"`
}
type workerAsset struct {
	Digest      string
	Size        int
	ContentType string
}
type workerCode struct {
	Digest            string                       `json:"digest"`
	ArchiveDigest     string                       `json:"archive_digest"`
	Size              int                          `json:"size"`
	CompatibilityDate string                       `json:"compatibilityDate"`
	MainModule        string                       `json:"mainModule"`
	Modules           map[string]map[string]string `json:"modules"`
	Assets            *WorkerAssetsConfig          `json:"assets,omitempty"`
	AssetFiles        map[string]workerAsset       `json:"asset_files,omitempty"`
	AssetBytes        map[string][]byte            `json:"-"`
}

// Only these extensions are served as assets; unknown extensions fail the build.
var workerAssetTypes = map[string]string{
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

func workerAssetType(name string) (string, bool) {
	kind, ok := workerAssetTypes[strings.ToLower(path.Ext(name))]
	return kind, ok
}

// Both query handling modes and pattern syntax are validated at submit time.
func validateWorkerAssets(c *WorkerAssetsConfig) error {
	if c == nil {
		return nil
	}
	if c.Directory == "" || path.IsAbs(c.Directory) || path.Clean(c.Directory) != c.Directory || c.Directory == "." || strings.HasPrefix(c.Directory, "../") {
		return problem(422, "WORKER_ASSETS_INVALID", "assets.directory must be a safe relative directory")
	}
	if len(c.RunWorkerFirst) > 16 {
		return problem(422, "WORKER_ASSETS_INVALID", "At most 16 run_worker_first patterns are supported")
	}
	seen := map[string]bool{}
	for _, p := range c.RunWorkerFirst {
		raw := strings.TrimPrefix(p, "!")
		if raw == "" || len(p) > 128 || !strings.HasPrefix(raw, "/") || strings.Contains(raw, "..") || strings.Contains(raw, "//") {
			return problem(422, "WORKER_ASSETS_INVALID", "Invalid run_worker_first pattern: "+p)
		}
		stars := strings.Count(raw, "*")
		if stars > 1 || stars == 1 && !strings.HasSuffix(raw, "*") {
			return problem(422, "WORKER_ASSETS_INVALID", "run_worker_first only supports a trailing *: "+p)
		}
		if seen[p] {
			return problem(422, "WORKER_ASSETS_INVALID", "Duplicate run_worker_first pattern: "+p)
		}
		seen[p] = true
	}
	if c.NotFoundHandling != "" && c.NotFoundHandling != "none" && c.NotFoundHandling != "single-page-application" {
		return problem(422, "WORKER_ASSETS_INVALID", "not_found_handling must be none or single-page-application")
	}
	if c.HTMLHandling != "" && c.HTMLHandling != "none" && c.HTMLHandling != "auto-trailing-slash" {
		return problem(422, "WORKER_ASSETS_INVALID", "html_handling must be none or auto-trailing-slash")
	}
	return nil
}

func workerAssetPath(name string) (string, bool) {
	if name == "" || strings.HasPrefix(name, "/") || path.Clean(name) != name || strings.HasPrefix(name, "../") || strings.Contains(name, "\\") {
		return "", false
	}
	return name, true
}

func bytesDigest(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }

// Parse the versioned profile. Schema 1 has no assets; schema 2 adds an assets/ tree.
// Paths, duplicates, links, sizes and all digests are checked before Loader sees code.
func parseWorkerArchive(raw []byte) (workerCode, string, error) {
	var out workerCode
	max := workerArchiveMax
	if len(raw) == 0 || len(raw) > workerArchiveAssetsMax {
		return out, "", errors.New("artifact size exceeds profile")
	}
	gz, e := gzip.NewReader(bytes.NewReader(raw))
	if e != nil {
		return out, "", errors.New("invalid gzip archive")
	}
	defer gz.Close()
	tr := tar.NewReader(io.LimitReader(gz, int64(workerArchiveAssetsMax)+1))
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
		if h.Typeflag != tar.TypeReg || h.Size < 0 || h.Size > workerAssetMax || files[h.Name] != nil {
			return out, "", errors.New("unsupported artifact entry")
		}
		allowed := h.Name == "manifest.json" || h.Name == "modules/index.js" || strings.HasPrefix(h.Name, "assets/")
		if !allowed {
			return out, "", errors.New("unsupported artifact entry")
		}
		data, e := io.ReadAll(io.LimitReader(tr, workerAssetMax+1))
		if e != nil || len(data) != int(h.Size) {
			return out, "", errors.New("invalid artifact file size")
		}
		total += len(data)
		if total > workerArchiveAssetsMax {
			return out, "", errors.New("expanded artifact too large")
		}
		files[h.Name] = data
	}
	// Consume gzip trailer too; a tar EOF is not sufficient integrity verification.
	tail, e := io.ReadAll(io.LimitReader(gz, 1025))
	if e != nil || len(tail) > 1024 {
		return out, "", errors.New("invalid gzip trailer or trailing payload")
	}
	if len(files) < 2 || files["manifest.json"] == nil || files["modules/index.js"] == nil {
		return out, "", errors.New("manifest and module required")
	}
	var m workerManifest
	decoder := json.NewDecoder(bytes.NewReader(files["manifest.json"]))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&m) != nil || (m.Schema != 1 && m.Schema != 2) || m.Profile != workerProfile || m.MainModule != "index.js" || len(m.Modules) != 1 {
		return out, "", errors.New("unsupported manifest")
	}
	module := m.Modules[0]
	code := files["modules/index.js"]
	if len(code) > workerModuleMax || module.Name != "index.js" || module.Path != "modules/index.js" || module.Type != "esm" || module.Size != len(code) || module.Digest != bytesDigest(code) {
		return out, "", errors.New("module digest mismatch")
	}
	out = workerCode{Digest: module.Digest, ArchiveDigest: bytesDigest(raw), Size: len(code), CompatibilityDate: "2026-09-28", MainModule: "index.js", Modules: map[string]map[string]string{"index.js": {"js": string(code)}}}
	if m.Schema == 1 {
		if m.Assets != nil {
			return out, "", errors.New("schema 1 cannot declare assets")
		}
		if len(files) != 2 || len(raw) > max {
			return out, "", errors.New("schema 1 artifact has unexpected entries")
		}
		return out, bytesDigest(files["manifest.json"]), nil
	}
	if m.Assets == nil {
		return out, "", errors.New("schema 2 requires assets")
	}
	if e := validateWorkerAssets(&WorkerAssetsConfig{Directory: m.Assets.Directory, RunWorkerFirst: m.Assets.RunWorkerFirst, NotFoundHandling: m.Assets.NotFoundHandling, HTMLHandling: m.Assets.HTMLHandling}); e != nil {
		return out, "", e
	}
	if len(m.Assets.Files) > workerAssetsFilesMax {
		return out, "", errors.New("too many asset files")
	}
	seen := map[string]bool{}
	totalAssets := 0
	assetFiles := map[string]workerAsset{}
	assetBytes := map[string][]byte{}
	for _, f := range m.Assets.Files {
		name, ok := workerAssetPath(f.Path)
		if !ok || seen[name] {
			return out, "", errors.New("invalid or duplicate asset path")
		}
		seen[name] = true
		data, ok := files["assets/"+name]
		if !ok {
			return out, "", errors.New("asset file missing from archive: " + name)
		}
		kind, ok := workerAssetType(name)
		if !ok || kind != f.ContentType {
			return out, "", errors.New("unsupported asset type: " + name)
		}
		if f.Size != len(data) || f.Digest != bytesDigest(data) || len(data) > workerAssetMax {
			return out, "", errors.New("asset digest mismatch: " + name)
		}
		totalAssets += len(data)
		if totalAssets > workerAssetsTotalMax {
			return out, "", errors.New("assets exceed total size limit")
		}
		assetFiles[name] = workerAsset{Digest: f.Digest, Size: f.Size, ContentType: f.ContentType}
		assetBytes[name] = data
	}
	if len(files) != len(m.Assets.Files)+2 {
		return out, "", errors.New("archive contains undeclared entries")
	}
	if m.Assets.NotFoundHandling == "single-page-application" && !seen["index.html"] {
		return out, "", errors.New("single-page-application requires index.html")
	}
	out.Assets = &WorkerAssetsConfig{Directory: m.Assets.Directory, RunWorkerFirst: m.Assets.RunWorkerFirst, NotFoundHandling: m.Assets.NotFoundHandling, HTMLHandling: m.Assets.HTMLHandling}
	out.AssetFiles = assetFiles
	out.AssetBytes = assetBytes
	return out, bytesDigest(files["manifest.json"]), nil
}
func newWorkerS3Endpoint(c *WorkersConfig, endpoint string) (*minio.Client, error) {
	u, _ := url.Parse(endpoint)
	client, e := minio.New(u.Host, &minio.Options{Creds: credentials.NewStaticV4(c.S3AccessKey, c.S3SecretKey, ""), Secure: u.Scheme == "https", Region: "us-east-1"})
	return client, e
}
func newWorkerS3(c *WorkersConfig) (*minio.Client, error) {
	return newWorkerS3Endpoint(c, c.S3Endpoint)
}
func (w *workersController) ensureBucket(ctx context.Context) error {
	ok, e := w.s3.BucketExists(ctx, w.config.S3Bucket)
	if e != nil {
		return e
	}
	if !ok {
		e = w.s3.MakeBucket(ctx, w.config.S3Bucket, minio.MakeBucketOptions{Region: "us-east-1"})
		if e != nil {
			ok, check := w.s3.BucketExists(ctx, w.config.S3Bucket)
			if check == nil && ok {
				return w.ensureAssetPolicy(ctx)
			}
		}
	}
	if e != nil {
		return e
	}
	return w.ensureAssetPolicy(ctx)
}

// Only pub/ and int/ are anonymous, plus the runtime-readable config prefix.
// The v1/ artifact segment (which contains code) always requires credentials.
func (w *workersController) ensureAssetPolicy(ctx context.Context) error {
	policy := fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":["*"]},"Action":["s3:GetObject"],"Resource":["arn:aws:s3:::%s/pub/*","arn:aws:s3:::%s/int/*","arn:aws:s3:::%s/config/*"]}]}`, w.config.S3Bucket, w.config.S3Bucket, w.config.S3Bucket)
	return w.s3.SetBucketPolicy(ctx, w.config.S3Bucket, policy)
}

func (w *workersController) getObject(ctx context.Context, key string) ([]byte, error) {
	obj, e := w.s3.GetObject(ctx, w.config.S3Bucket, key, minio.GetObjectOptions{})
	if e != nil {
		return nil, e
	}
	defer obj.Close()
	raw, e := io.ReadAll(io.LimitReader(obj, workerArchiveAssetsMax+1))
	if e != nil {
		return nil, e
	}
	if len(raw) > workerArchiveAssetsMax {
		return nil, errors.New("artifact too large")
	}
	return raw, nil
}
func (w *workersController) putImmutable(ctx context.Context, key string, raw []byte) error {
	obj, e := w.s3.StatObject(ctx, w.config.S3Bucket, key, minio.StatObjectOptions{})
	if e == nil {
		if obj.Size != int64(len(raw)) {
			return errors.New("immutable object size conflict")
		}
		existing, e := w.getObject(ctx, key)
		if e != nil || bytesDigest(existing) != bytesDigest(raw) {
			return errors.New("immutable object content conflict")
		}
		return nil
	}
	if minio.ToErrorResponse(e).Code != "NoSuchKey" {
		return e
	}
	options := minio.PutObjectOptions{ContentType: "application/gzip"}
	options.SetMatchETagExcept("*")
	e = w.putBytes(ctx, key, raw, options)
	if minio.ToErrorResponse(e).Code == "PreconditionFailed" {
		existing, readErr := w.getObject(ctx, key)
		if readErr == nil && bytesDigest(existing) == bytesDigest(raw) {
			return nil
		}
		return errors.New("immutable object conflict")
	}
	return e
}
func (w *workersController) builderS3() *minio.Client {
	if w.config.BuilderMode == "local" {
		return w.s3
	}
	return w.s3Builder
}

func (w *workersController) uploadURL(ctx context.Context, b WorkerBuild) (string, error) {
	// Sign with the builder-visible endpoint so in-cluster Pods can reach (and verify) the URL.
	u, e := w.builderS3().PresignedPutObject(ctx, w.config.S3Bucket, workerUploadKey(b), 15*time.Minute)
	if e != nil {
		return "", e
	}
	return u.String(), nil
}

// Runtimes live in the same network position as builders, so artifact GET URLs
// are signed against the same in-cluster endpoint. Signing is offline.
func (w *workersController) downloadURL(ctx context.Context, a WorkerArtifact) (string, error) {
	if a.StorageLocation != w.config.S3LocationID || a.Bucket != w.config.S3Bucket {
		return "", errors.New("artifact storage location mismatch")
	}
	u, e := w.s3Builder.PresignedGetObject(ctx, w.config.S3Bucket, a.ObjectKey, 15*time.Minute, nil)
	if e != nil {
		return "", e
	}
	return u.String(), nil
}
func workerUploadKey(b WorkerBuild) string {
	return "uploads/" + b.Organization + "/" + b.ID + "/bundle.tar.gz"
}

func workerSourceUploadKey(organization, id string) string {
	return "uploads/" + organization + "/src/" + id + "/source.tar.gz"
}

func validWorkerSourceUpload(organization, key string) bool {
	return key != "" && strings.HasPrefix(key, "uploads/"+organization+"/src/") && strings.HasSuffix(key, "/source.tar.gz") && !strings.Contains(key, "..") && len(key) <= 256
}

// Clients PUT the source bundle with this URL; it is signed against the public
// endpoint because the client is outside the cluster.
func (w *workersController) sourceUploadURL(ctx context.Context, organization string) (string, string, error) {
	id := randomID("src")
	key := workerSourceUploadKey(organization, id)
	u, e := w.s3.PresignedPutObject(ctx, w.config.S3Bucket, key, 15*time.Minute)
	if e != nil {
		return "", "", e
	}
	return key, u.String(), nil
}

// Builder and runtime live inside the cluster, so read URLs are signed against
// the in-cluster endpoint. Signing is offline.
func (w *workersController) sourceBundleURL(ctx context.Context, key string) (string, error) {
	u, e := w.builderS3().PresignedGetObject(ctx, w.config.S3Bucket, key, 15*time.Minute, nil)
	if e != nil {
		return "", e
	}
	return u.String(), nil
}

// Source bundles have two top-level sections: src/ (Worker code) and assets/
// (files relative to assets.directory). The control plane produces the same
// format for repository and inline builds.
func workerSourceBundle(code map[string]string, assets map[string][]byte) ([]byte, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	write := func(name string, raw []byte) error {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(raw)), Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		_, err := tw.Write(raw)
		return err
	}
	names := make([]string, 0, len(code))
	for name := range code {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := write("src/"+name, []byte(code[name])); err != nil {
			return nil, err
		}
	}
	assetNames := make([]string, 0, len(assets))
	for name := range assets {
		assetNames = append(assetNames, name)
	}
	sort.Strings(assetNames)
	for _, name := range assetNames {
		if err := write("assets/"+name, assets[name]); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	if buf.Len() > workerSourceMax {
		return nil, errors.New("source bundle exceeds size limit")
	}
	return buf.Bytes(), nil
}

func workerAssetObjectKey(prefix, organization, service, archiveDigest, name string) string {
	return prefix + "/" + organization + "/" + service + "/" + archiveDigest + "/" + name
}

var workerHashedAsset = regexp.MustCompile(`[-.][0-9a-f]{8,}\.[a-z0-9]+$`)

func workerAssetCacheControl(name string) string {
	if workerHashedAsset.MatchString(name) {
		return "public, max-age=31536000, immutable"
	}
	return "public, max-age=300"
}

// pub/ is the public static origin, int/ the internal one. Both copies come from
// the same immutable archive; the private v1/ artifact key is never anonymous.
func (w *workersController) putAssetObjects(ctx context.Context, b WorkerBuild, a WorkerArtifact, code workerCode) error {
	if code.AssetBytes == nil {
		return nil
	}
	for name, raw := range code.AssetBytes {
		meta := code.AssetFiles[name]
		for _, prefix := range []string{"pub", "int"} {
			key := workerAssetObjectKey(prefix, b.Organization, b.Service, a.ArchiveDigest, name)
			options := minio.PutObjectOptions{ContentType: meta.ContentType, CacheControl: workerAssetCacheControl(name)}
			if e := w.putBytes(ctx, key, raw, options); e != nil {
				return e
			}
		}
	}
	return nil
}
func (w *workersController) finalizeBuild(ctx context.Context, b WorkerBuild) (WorkerArtifact, error) {
	raw, e := w.getObject(ctx, workerUploadKey(b))
	if e != nil {
		return WorkerArtifact{}, e
	}
	code, manifest, e := parseWorkerArchive(raw)
	if e != nil {
		return WorkerArtifact{}, e
	}
	a := WorkerArtifact{ID: "art_" + digest(b.Organization+"/"+code.ArchiveDigest), Organization: b.Organization, State: "finalized", StorageLocation: w.config.S3LocationID, Bucket: w.config.S3Bucket, ArchiveDigest: code.ArchiveDigest, ManifestDigest: manifest, ModuleDigest: code.Digest, Size: len(raw), ObjectKey: "v1/" + b.Organization + "/sha256/" + code.ArchiveDigest + "/bundle.tar.gz", Created: now()}
	if e := w.putImmutable(ctx, a.ObjectKey, raw); e != nil {
		return a, e
	}
	// Per-file copies exist so kgateway and CDN can serve assets directly; the
	// archive above remains the runtime's single source of truth.
	if e := w.putAssetObjects(ctx, b, a, code); e != nil {
		return a, e
	}
	return a, nil
}

// artifactCode stays as the control plane's own integrity check when promoting
// a build and validating a release candidate; runtime cold loads verify the
// same digests locally after downloading via the presigned URL.
func (w *workersController) artifactCode(ctx context.Context, a WorkerArtifact) (workerCode, error) {
	if a.StorageLocation != w.config.S3LocationID || a.Bucket != w.config.S3Bucket {
		return workerCode{}, errors.New("artifact storage location mismatch")
	}
	raw, e := w.getObject(ctx, a.ObjectKey)
	if e != nil {
		return workerCode{}, e
	}
	code, manifest, e := parseWorkerArchive(raw)
	if e != nil {
		return code, e
	}
	if code.ArchiveDigest != a.ArchiveDigest || code.Digest != a.ModuleDigest || manifest != a.ManifestDigest || len(raw) != a.Size {
		return code, fmt.Errorf("artifact metadata mismatch")
	}
	return code, nil
}
func safeWorkerLog(text string) string {
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
