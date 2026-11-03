package objectstore

import (
	"errors"
	"net/url"
	"strings"

	appconfig "github.com/krun-dev/krun/internal/config"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

func NewWorkerS3Endpoint(c *appconfig.WorkersConfig, endpoint string) (Client, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return nil, errors.New("invalid object storage endpoint")
	}
	region := c.S3Region
	if region == "" {
		region = "us-east-1"
	}
	client, err := minio.New(u.Host, &minio.Options{
		Creds:  credentials.NewStaticV4(c.S3AccessKey, c.S3SecretKey, ""),
		Secure: u.Scheme == "https",
		Region: region,
		BucketLookupViaURL: func(endpoint url.URL, bucket string) minio.BucketLookupType {
			if c.S3UsesVirtualHost(endpoint, bucket) {
				return minio.BucketLookupDNS
			}
			return minio.BucketLookupPath
		},
	})
	if err != nil {
		return nil, err
	}
	// Keep IPv6/dual-stack endpoint selection explicit in application config.
	client.SetS3EnableDualstack(strings.Contains(u.Hostname(), ".dualstack."))

	if c.S3Prefix != "" {
		return &prefixedClient{Client: client, prefix: c.S3Prefix + "/"}, nil
	}
	return client, nil
}

func NewWorkerS3(c *appconfig.WorkersConfig) (Client, error) {
	return NewWorkerS3Endpoint(c, c.S3Endpoint)
}
