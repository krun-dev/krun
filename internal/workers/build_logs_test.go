package workers

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/krun-dev/krun/internal/adapters/objectstore"
	"github.com/krun-dev/krun/internal/config"
)

func TestBuildLogSnapshotsArePrivateBoundedAndOwnerScoped(t *testing.T) {
	var contents string
	w := &Controller{Config: &config.WorkersConfig{S3BucketMode: "external", S3Bucket: "bucket"}}
	w.PutObject = func(_ context.Context, bucket, key string, r io.Reader, size int64, opts objectstore.PutObjectOptions) (objectstore.UploadInfo, error) {
		if bucket != "bucket" || !strings.HasPrefix(key, "build-logs/b/") || opts.ContentType != "text/plain; charset=utf-8" {
			t.Fatal("invalid object metadata")
		}
		raw, _ := io.ReadAll(r)
		contents = string(raw)
		return objectstore.UploadInfo{}, nil
	}
	ctx := context.Background()
	first, err := w.SaveBuildLog(ctx, "b", "old", "https://object.test/?X-Amz-Signature=secret")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(contents, "secret") {
		t.Fatal("signed URL persisted")
	}
	second, err := w.SaveBuildLog(ctx, "b", "new", "https://object.test/?X-Amz-Signature=secret")
	if err != nil || first == second {
		t.Fatal("lease owners share a mutable object")
	}
	_, err = w.SaveBuildLog(ctx, "b", "new", strings.Repeat("x", 100000))
	if err != nil || len(contents) > 32768 {
		t.Fatal("unbounded log")
	}
}
