package workers

import (
	"context"
	"net/url"
	"path"
	"time"

	"github.com/krun-dev/krun/internal/domain"
)

type ReadCredentialSource interface {
	Credentials(context.Context, domain.StorageCredentialScope, time.Duration) (domain.StorageCredentials, error)
}

// ReadGrant must only be called after the caller authorizes the immutable
// artifact or static origin. The issuer restricts reads to this exact directory.
func (w *Controller) ReadGrant(ctx context.Context, organization, resource, prefix string) (domain.StorageReadGrant, error) {
	if w.ReadCredentials == nil {
		return domain.StorageReadGrant{}, domain.Problem(503, "STORAGE_CREDENTIALS_UNAVAILABLE", "Runtime storage credentials unavailable")
	}
	credentials, err := w.ReadCredentials.Credentials(ctx, domain.StorageCredentialScope{
		Organization:          organization,
		BucketID:              resource,
		StoreID:               "artifacts",
		Bucket:                w.Config.S3Bucket,
		Prefix:                w.Config.S3Key(prefix),
		Access:                domain.StorageRead,
		AuthorizationRevision: 1,
	}, 2*time.Minute)
	if err != nil {
		return domain.StorageReadGrant{}, domain.Problem(503, "STORAGE_CREDENTIALS_UNAVAILABLE", "Runtime storage credentials unavailable")
	}
	endpoint, err := url.Parse(w.Config.S3RuntimeEndpoint)
	if err != nil {
		return domain.StorageReadGrant{}, domain.Problem(503, "STORAGE_UNAVAILABLE", "Runtime storage unavailable")
	}
	style := "path"
	if w.Config.S3UsesVirtualHost(*endpoint, w.Config.S3Bucket) {
		style = "virtual"
	}
	return domain.StorageReadGrant{
		Endpoint:        w.Config.S3RuntimeEndpoint,
		Region:          w.Config.S3Region,
		Bucket:          w.Config.S3Bucket,
		Prefix:          w.Config.S3Key(prefix),
		AddressingStyle: style,
		LeaseUntil:      time.Now().UTC().Add(5 * time.Minute),
		Credentials:     credentials,
	}, nil
}

func (w *Controller) ArtifactReadGrant(ctx context.Context, artifact domain.WorkerArtifact) (domain.StorageReadGrant, error) {
	if artifact.StorageLocation != w.Config.S3LocationID || artifact.Bucket != w.Config.S3Bucket ||
		artifact.ObjectKey != "v1/"+artifact.Organization+"/sha256/"+artifact.ArchiveDigest+"/bundle.tar.gz" {
		return domain.StorageReadGrant{}, domain.NotFound()
	}
	return w.ReadGrant(ctx, artifact.Organization, artifact.ID, path.Dir(artifact.ObjectKey)+"/")
}
