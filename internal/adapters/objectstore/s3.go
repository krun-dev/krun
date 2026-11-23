package objectstore

import (
	"errors"
	"net/url"
	"strings"

	"github.com/krun-dev/krun/internal/adapters/storagecredentials"
	appconfig "github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

func NewWorkerS3Endpoint(c *appconfig.WorkersConfig, endpoint string) (Client, error) {
	if !c.ArtifactUsesSTS() {
		return newS3(c, endpoint, credentials.NewStaticV4(c.ArtifactStorage.Credentials.AccessKeyID, c.ArtifactStorage.Credentials.SecretAccessKey, ""))
	}
	if err := c.ValidateArtifactSTSEndpoint(endpoint); err != nil {
		return nil, err
	}
	manager, err := storagecredentials.NewArtifactManager(c)
	if err != nil {
		return nil, err
	}
	scope := domain.StorageCredentialScope{
		Organization: "control-plane", BucketID: c.ArtifactStorage.LocationID, StoreID: "artifacts",
		Bucket: c.ArtifactStorage.Bucket, Prefix: c.ArtifactStorage.Prefix + "/", Access: domain.StorageAllAccess,
		AuthorizationRevision: 1,
	}
	return newSTSS3(c, endpoint, manager, scope)
}

func newS3(c *appconfig.WorkersConfig, endpoint string, creds *credentials.Credentials) (Client, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return nil, errors.New("invalid object storage endpoint")
	}
	region := c.ArtifactStorage.Region
	if region == "" {
		region = "us-east-1"
	}
	client, err := minio.New(u.Host, &minio.Options{
		Creds:  creds,
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

	if c.ArtifactStorage.Prefix != "" {
		return &prefixedClient{Client: client, prefix: c.ArtifactStorage.Prefix + "/"}, nil
	}
	return client, nil
}

func NewWorkerS3(c *appconfig.WorkersConfig) (Client, error) {
	return NewWorkerS3Endpoint(c, c.ArtifactStorage.Endpoint)
}
