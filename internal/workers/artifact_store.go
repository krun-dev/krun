package workers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	objectstore "github.com/krun-dev/krun/internal/adapters/objectstore"
	workerartifact "github.com/krun-dev/krun/internal/artifact"
	domain "github.com/krun-dev/krun/internal/domain"
)

// putBytes writes a whole object through the injected storage hook.
func (w *Controller) PutBytes(ctx context.Context, key string, data []byte, opts objectstore.PutObjectOptions) error {
	if w.PutObject == nil {
		return errors.New("object storage unavailable")
	}
	_, e := w.PutObject(ctx, w.Config.S3Bucket, key, bytes.NewReader(data), int64(len(data)), opts)
	return e
}

var NewWorkerS3Endpoint = objectstore.NewWorkerS3Endpoint

var NewWorkerS3 = objectstore.NewWorkerS3

func (w *Controller) EnsureBucket(ctx context.Context) error {
	if w.Config.S3BucketMode == "external" {
		return nil
	}
	ok, e := w.S3.BucketExists(ctx, w.Config.S3Bucket)
	if e != nil {
		return e
	}
	if !ok {
		e = w.S3.MakeBucket(ctx, w.Config.S3Bucket, objectstore.MakeBucketOptions{Region: w.Config.S3Region})
		if e != nil {
			ok, check := w.S3.BucketExists(ctx, w.Config.S3Bucket)
			if check == nil && ok {
				return w.EnsureAssetPolicy(ctx)
			}
		}
	}
	if e != nil {
		return e
	}
	return w.EnsureAssetPolicy(ctx)
}

// Only pub/ and int/ are anonymous, plus the runtime-readable config prefix.
// The v1/ artifact segment (which contains code) always requires credentials.
func (w *Controller) EnsureAssetPolicy(ctx context.Context) error {
	if w.Config.S3BucketMode == "external" || w.Config.S3UsesSignedAccess() {
		return nil
	}
	policy := fmt.Sprintf(`{
  "Version": "2012-10-17",
  "Statement": [{
   "Effect": "Allow",
   "Principal": {"AWS": ["*"]},
   "Action": ["s3:GetObject"],
   "Resource": [
    "arn:aws:s3:::%s/*",
    "arn:aws:s3:::%s/*",
    "arn:aws:s3:::%s/*"
   ]
  }]
 }`,
		w.Config.S3Bucket+"/"+w.Config.S3Key("pub"),
		w.Config.S3Bucket+"/"+w.Config.S3Key("int"),
		w.Config.S3Bucket+"/"+w.Config.S3Key("config"),
	)
	return w.S3.SetBucketPolicy(ctx, w.Config.S3Bucket, policy)
}

func (w *Controller) GetObject(ctx context.Context, key string) ([]byte, error) {
	obj, e := w.S3.GetObject(ctx, w.Config.S3Bucket, key, objectstore.GetObjectOptions{})
	if e != nil {
		return nil, e
	}
	defer obj.Close()
	raw, e := io.ReadAll(io.LimitReader(obj, workerartifact.WorkerArchiveAssetsMax+1))
	if e != nil {
		return nil, e
	}
	if len(raw) > workerartifact.WorkerArchiveAssetsMax {
		return nil, errors.New("artifact too large")
	}
	return raw, nil
}

func (w *Controller) PutImmutable(ctx context.Context, key string, raw []byte) error {
	obj, e := w.S3.StatObject(ctx, w.Config.S3Bucket, key, objectstore.StatObjectOptions{})
	if e == nil {
		if obj.Size != int64(len(raw)) {
			return errors.New("immutable object size conflict")
		}
		existing, e := w.GetObject(ctx, key)
		if e != nil || workerartifact.BytesDigest(existing) != workerartifact.BytesDigest(raw) {
			return errors.New("immutable object content conflict")
		}
		return nil
	}
	if objectstore.ToErrorResponse(e).Code != "NoSuchKey" {
		return e
	}
	options := objectstore.PutObjectOptions{ContentType: "application/gzip"}
	options.SetMatchETagExcept("*")
	e = w.PutBytes(ctx, key, raw, options)
	if objectstore.ToErrorResponse(e).Code == "PreconditionFailed" {
		existing, readErr := w.GetObject(ctx, key)
		if readErr == nil && workerartifact.BytesDigest(existing) == workerartifact.BytesDigest(raw) {
			return nil
		}
		return errors.New("immutable object conflict")
	}
	return e
}

func (w *Controller) BuilderS3() objectstore.Client {
	if w.Config.BuilderMode == "local" {
		return w.S3
	}
	return w.S3Builder
}

func (w *Controller) UploadURL(ctx context.Context, b domain.WorkerBuild) (string, error) {

	u, e := w.BuilderS3().PresignedPutObject(ctx, w.Config.S3Bucket, WorkerUploadKey(b), w.BuildURLTTL())
	if e != nil {
		return "", e
	}
	return u.String(), nil
}

func (w *Controller) BuildURLTTL() time.Duration {
	seconds := w.Config.BuilderTimeoutSeconds
	if seconds == 0 {
		seconds = 600
	}

	return time.Duration(seconds+300) * time.Second
}

// Runtime artifact URLs use the runtime's own network view. In a full
// in-cluster deployment this normally equals the builder endpoint; local-first
// keeps the builder local while the runtime reaches MinIO through a selectorless
// Service, so the two endpoints must be independently configurable.
func (w *Controller) DownloadURL(ctx context.Context, a domain.WorkerArtifact) (string, error) {
	if a.StorageLocation != w.Config.S3LocationID || a.Bucket != w.Config.S3Bucket {
		return "", errors.New("artifact storage location mismatch")
	}
	u, e := w.S3Runtime.PresignedGetObject(ctx, w.Config.S3Bucket, a.ObjectKey, 15*time.Minute, nil)
	if e != nil {
		return "", e
	}
	return u.String(), nil
}

func WorkerUploadKey(b domain.WorkerBuild) string {
	return "uploads/" + b.Organization + "/" + b.ID + "/bundle.tar.gz"
}

func WorkerAssetObjectKey(prefix, organization, worker, archiveDigest, name string) string {
	return prefix + "/" + organization + "/" + worker + "/" + archiveDigest + "/" + name
}

func WorkerAssetCacheControl(name string) string {
	if workerartifact.WorkerHashedAsset.MatchString(name) {
		return "public, max-age=31536000, immutable"
	}
	return "public, max-age=300"
}

// pub/ is the public static origin, int/ the internal one. Both copies come from
// the same immutable archive; the private v1/ artifact key is never anonymous.
func (w *Controller) PutAssetObjects(ctx context.Context, b domain.WorkerBuild, a domain.WorkerArtifact, code workerartifact.WorkerCode) error {
	keys := []string{}
	for name := range code.AssetBytes {
		for _, prefix := range []string{"pub", "int"} {
			keys = append(keys, WorkerAssetObjectKey(prefix, b.Organization, b.Worker, a.ArchiveDigest, name))
		}
	}
	if err := w.Service.Store.RegisterAssetObjects(ctx, a.ID, keys); err != nil {
		return err
	}
	tasks := make(chan string, len(keys))
	for _, key := range keys {
		tasks <- key
	}
	close(tasks)
	var wg sync.WaitGroup
	errs := make(chan error, len(keys))
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for key := range tasks {
				_, tail, _ := strings.Cut(key, "/"+a.ArchiveDigest+"/")
				meta := code.AssetFiles[tail]
				options := objectstore.PutObjectOptions{
					ContentType:  meta.ContentType,
					CacheControl: WorkerAssetCacheControl(tail),
					UserMetadata: map[string]string{"krun-sha256": meta.Digest},
				}
				if err := w.PutBytes(ctx, key, code.AssetBytes[tail], options); err != nil {
					errs <- err
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	var failures []error
	for err := range errs {
		failures = append(failures, err)
	}
	return errors.Join(failures...)
}

// StaticAsset reads only immutable per-file asset metadata. Static domains have
// the same visibility as the former pub/int origins, independently of pool placement.
func (w *Controller) StaticAsset(ctx context.Context, prefix, organization, worker, digest, name string) (workerartifact.WorkerManifestFile, error) {
	key := WorkerAssetObjectKey(prefix, organization, worker, digest, name)
	info, err := w.S3.StatObject(ctx, w.Config.S3Bucket, key, objectstore.StatObjectOptions{})
	if err != nil {
		if objectstore.ToErrorResponse(err).Code == "NoSuchKey" {
			return workerartifact.WorkerManifestFile{}, domain.NotFound()
		}
		return workerartifact.WorkerManifestFile{}, err
	}
	hash := ""
	for key, value := range info.UserMetadata {
		if strings.EqualFold(key, "krun-sha256") {
			hash = value
		}
	}
	if len(hash) != 64 || strings.Trim(hash, "0123456789abcdef") != "" || info.Size < 0 || info.Size > workerartifact.WorkerAssetMax {
		return workerartifact.WorkerManifestFile{}, domain.NotFound()
	}
	kind, _ := workerartifact.WorkerAssetType(name)
	return workerartifact.WorkerManifestFile{
		Path:        name,
		Digest:      hash,
		Size:        int(info.Size),
		ContentType: kind,
	}, nil
}
