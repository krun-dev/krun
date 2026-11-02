package workers

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"path"
	"sort"
	"strings"
	"time"

	objectstore "github.com/krun-dev/krun/internal/adapters/objectstore"
	workerartifact "github.com/krun-dev/krun/internal/artifact"
	securetoken "github.com/krun-dev/krun/internal/securetoken"
)

func WorkerSourceUploadKey(organization, id string) string {
	return "uploads/" + organization + "/src/" + id + "/source.tar.gz"
}

func ValidWorkerSourceUpload(organization, key string) bool {
	return key != "" && strings.HasPrefix(key, "uploads/"+organization+"/src/") && strings.HasSuffix(key, "/source.tar.gz") && !strings.Contains(key, "..") && len(key) <= 256
}

// Clients PUT the source bundle with this URL; it is signed against the public
// endpoint because the client is outside the cluster.
func (w *Controller) SourceUploadURL(ctx context.Context, organization string) (string, string, error) {
	id := securetoken.NewID("src")
	key := WorkerSourceUploadKey(organization, id)
	if e := w.Service.Store.IssueWorkerSourceUpload(ctx, key, organization); e != nil {
		return "", "", e
	}
	u, e := w.S3.PresignedPutObject(ctx, w.Config.S3Bucket, key, 15*time.Minute)
	if e != nil {
		return "", "", e
	}
	return key, u.String(), nil
}

// Builder and runtime live inside the cluster, so read URLs are signed against
// the in-cluster endpoint. Signing is offline.
func (w *Controller) SourceBundleURL(ctx context.Context, key string) (string, error) {
	info, e := w.S3.StatObject(ctx, w.Config.S3Bucket, key, objectstore.StatObjectOptions{})
	if e != nil {
		return "", e
	}
	if info.Size <= 0 || info.Size > workerartifact.WorkerSourceMax {
		return "", errors.New("source bundle exceeds size limit")
	}
	u, e := w.BuilderS3().PresignedGetObject(ctx, w.Config.S3Bucket, key, w.BuildURLTTL(), nil)
	if e != nil {
		return "", e
	}
	return u.String(), nil
}

// Source bundles have two top-level sections: src/ (Worker code) and assets/
// (files relative to assets.directory). The control plane produces the same
// format for repository and inline builds.
func WorkerSourceBundle(code map[string]string, assets map[string][]byte) ([]byte, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	write := func(name string, raw []byte) error {
		if len(name) > 255 || len(path.Base(name)) > 100 {
			return errors.New("source bundle path exceeds portable ustar limit")
		}
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(raw)), Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}); err != nil {
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
	if buf.Len() > workerartifact.WorkerSourceMax {
		return nil, errors.New("source bundle exceeds size limit")
	}
	return buf.Bytes(), nil
}
