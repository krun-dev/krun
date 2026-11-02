package objectstore

import (
	"net/url"

	appconfig "github.com/krun-dev/krun/internal/config"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

func NewWorkerS3Endpoint(c *appconfig.WorkersConfig, endpoint string) (Client, error) {
	u, _ := url.Parse(endpoint)
	client, e := minio.New(u.Host, &minio.Options{Creds: credentials.NewStaticV4(c.S3AccessKey, c.S3SecretKey, ""), Secure: u.Scheme == "https", Region: "us-east-1"})
	return client, e
}

func NewWorkerS3(c *appconfig.WorkersConfig) (Client, error) {
	return NewWorkerS3Endpoint(c, c.S3Endpoint)
}
