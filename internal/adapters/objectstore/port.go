package objectstore

import (
	"context"
	"io"
	"net/url"
	"time"

	"github.com/minio/minio-go/v7"
)

// Client is the object-storage port used by build and config workflows. Native
// MinIO clients implement it; the application never owns transport setup.
type Client interface {
	BucketExists(context.Context, string) (bool, error)
	MakeBucket(context.Context, string, MakeBucketOptions) error
	SetBucketPolicy(context.Context, string, string) error
	GetObject(context.Context, string, string, GetObjectOptions) (*minio.Object, error)
	StatObject(context.Context, string, string, StatObjectOptions) (ObjectInfo, error)
	PutObject(context.Context, string, string, io.Reader, int64, PutObjectOptions) (UploadInfo, error)
	RemoveObject(context.Context, string, string, RemoveObjectOptions) error
	ListObjects(context.Context, string, ListObjectsOptions) <-chan ObjectInfo
	RemoveObjects(context.Context, string, <-chan ObjectInfo, RemoveObjectsOptions) <-chan minio.RemoveObjectError
	PresignedPutObject(context.Context, string, string, time.Duration) (*url.URL, error)
	PresignedGetObject(context.Context, string, string, time.Duration, url.Values) (*url.URL, error)
}
type MakeBucketOptions = minio.MakeBucketOptions
type GetObjectOptions = minio.GetObjectOptions
type StatObjectOptions = minio.StatObjectOptions
type PutObjectOptions = minio.PutObjectOptions
type UploadInfo = minio.UploadInfo
type RemoveObjectOptions = minio.RemoveObjectOptions
type RemoveObjectsOptions = minio.RemoveObjectsOptions
type ListObjectsOptions = minio.ListObjectsOptions
type ObjectInfo = minio.ObjectInfo

var ToErrorResponse = minio.ToErrorResponse
var _ Client = (*minio.Client)(nil)
