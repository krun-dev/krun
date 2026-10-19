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
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

const workerArchiveMax = 8 << 20
const workerModuleMax = 2 << 20
const workerProfile = "esbuild-esm-v1"

type workerManifest struct {
	Schema     int                    `json:"schema_version"`
	Profile    string                 `json:"profile"`
	MainModule string                 `json:"main_module"`
	Modules    []workerManifestModule `json:"modules"`
}
type workerManifestModule struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Type   string `json:"type"`
	Digest string `json:"sha256"`
	Size   int    `json:"size_bytes"`
}
type workerCode struct {
	Digest            string                       `json:"digest"`
	ArchiveDigest     string                       `json:"archive_digest"`
	Size              int                          `json:"size"`
	CompatibilityDate string                       `json:"compatibilityDate"`
	MainModule        string                       `json:"mainModule"`
	Modules           map[string]map[string]string `json:"modules"`
}

func bytesDigest(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }

// Parse only the versioned two-file profile. Paths, duplicates, links, sizes and all digests are checked before Loader sees code.
func parseWorkerArchive(raw []byte) (workerCode, string, error) {
	var out workerCode
	if len(raw) == 0 || len(raw) > workerArchiveMax {
		return out, "", errors.New("artifact size exceeds profile")
	}
	gz, e := gzip.NewReader(bytes.NewReader(raw))
	if e != nil {
		return out, "", errors.New("invalid gzip archive")
	}
	defer gz.Close()
	tr := tar.NewReader(io.LimitReader(gz, workerArchiveMax+1))
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
		if h.Typeflag != tar.TypeReg || h.Size < 0 || h.Size > workerModuleMax || !contains([]string{"manifest.json", "modules/index.js"}, h.Name) || files[h.Name] != nil {
			return out, "", errors.New("unsupported artifact entry")
		}
		data, e := io.ReadAll(io.LimitReader(tr, workerModuleMax+1))
		if e != nil || len(data) != int(h.Size) {
			return out, "", errors.New("invalid artifact file size")
		}
		total += len(data)
		if total > workerArchiveMax {
			return out, "", errors.New("expanded artifact too large")
		}
		files[h.Name] = data
	}
	// Consume gzip trailer too; a tar EOF is not sufficient integrity verification.
	tail, e := io.ReadAll(io.LimitReader(gz, 1025))
	if e != nil || len(tail) > 1024 {
		return out, "", errors.New("invalid gzip trailer or trailing payload")
	}
	if len(files) != 2 {
		return out, "", errors.New("manifest and module required")
	}
	var m workerManifest
	decoder := json.NewDecoder(bytes.NewReader(files["manifest.json"]))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&m) != nil || m.Schema != 1 || m.Profile != workerProfile || m.MainModule != "index.js" || len(m.Modules) != 1 {
		return out, "", errors.New("unsupported manifest")
	}
	module := m.Modules[0]
	code := files["modules/index.js"]
	if module.Name != "index.js" || module.Path != "modules/index.js" || module.Type != "esm" || module.Size != len(code) || module.Digest != bytesDigest(code) {
		return out, "", errors.New("module digest mismatch")
	}
	out = workerCode{Digest: module.Digest, ArchiveDigest: bytesDigest(raw), Size: len(code), CompatibilityDate: "2026-09-28", MainModule: "index.js", Modules: map[string]map[string]string{"index.js": {"js": string(code)}}}
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
	if ok {
		return nil
	}
	e = w.s3.MakeBucket(ctx, w.config.S3Bucket, minio.MakeBucketOptions{Region: "us-east-1"})
	if e != nil {
		ok, check := w.s3.BucketExists(ctx, w.config.S3Bucket)
		if check == nil && ok {
			return nil
		}
	}
	return e
}
func (w *workersController) getObject(ctx context.Context, key string) ([]byte, error) {
	obj, e := w.s3.GetObject(ctx, w.config.S3Bucket, key, minio.GetObjectOptions{})
	if e != nil {
		return nil, e
	}
	defer obj.Close()
	raw, e := io.ReadAll(io.LimitReader(obj, workerArchiveMax+1))
	if e != nil {
		return nil, e
	}
	if len(raw) > workerArchiveMax {
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
	_, e = w.s3.PutObject(ctx, w.config.S3Bucket, key, bytes.NewReader(raw), int64(len(raw)), options)
	if minio.ToErrorResponse(e).Code == "PreconditionFailed" {
		existing, readErr := w.getObject(ctx, key)
		if readErr == nil && bytesDigest(existing) == bytesDigest(raw) {
			return nil
		}
		return errors.New("immutable object conflict")
	}
	return e
}
func (w *workersController) uploadURL(ctx context.Context, b WorkerBuild) (string, error) {
	// Sign with the builder-visible endpoint so in-cluster Pods can reach (and verify) the URL.
	u, e := w.s3Builder.PresignedPutObject(ctx, w.config.S3Bucket, workerUploadKey(b), 15*time.Minute)
	if e != nil {
		return "", e
	}
	return u.String(), nil
}
func workerUploadKey(b WorkerBuild) string {
	return "uploads/" + b.Organization + "/" + b.ID + "/bundle.tar.gz"
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
	return a, w.putImmutable(ctx, a.ObjectKey, raw)
}
func (w *workersController) artifactCode(ctx context.Context, a WorkerArtifact) (workerCode, error) {
	if (a.StorageLocation != "" && a.StorageLocation != w.config.S3LocationID) || (a.Bucket != "" && a.Bucket != w.config.S3Bucket) {
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
