package objectstore

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type sessionSource interface {
	Credentials(context.Context, domain.StorageCredentialScope, time.Duration) (domain.StorageCredentials, error)
}

type roleProvider struct {
	source sessionSource
	scope  domain.StorageCredentialScope
	expiry credentials.Expiry
}

func (p *roleProvider) Retrieve() (credentials.Value, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	session, err := p.source.Credentials(ctx, p.scope, 30*time.Second)
	if err != nil {
		return credentials.Value{}, err
	}
	p.expiry.SetExpiration(session.ExpiresAt, 2*time.Minute)
	return credentials.Value{
		AccessKeyID: session.AccessKeyID, SecretAccessKey: session.SecretAccessKey,
		SessionToken: session.SessionToken, SignerType: credentials.SignatureV4,
	}, nil
}

func (p *roleProvider) RetrieveWithCredContext(*credentials.CredContext) (credentials.Value, error) {
	return p.Retrieve()
}

func (p *roleProvider) IsExpired() bool { return p.expiry.IsExpired() }

type stsClient struct {
	Client
	config   *config.WorkersConfig
	endpoint string
	source   sessionSource
	scope    domain.StorageCredentialScope
}

func newSTSS3(c *config.WorkersConfig, endpoint string, source sessionSource, scope domain.StorageCredentialScope) (Client, error) {
	client, err := newS3(c, endpoint, credentials.New(&roleProvider{source: source, scope: scope}))
	if err != nil {
		return nil, err
	}
	return &stsClient{Client: client, config: c, endpoint: endpoint, source: source, scope: scope}, nil
}

// A presigned URL must remain usable for its advertised lifetime. Pin the
// selected session to this signing operation so refresh cannot change it.
func (c *stsClient) presigner(ctx context.Context, ttl time.Duration) (Client, error) {
	if ttl < time.Second || ttl >= time.Hour-30*time.Second {
		return nil, fmt.Errorf("artifact STS presigned URL lifetime must be between 1 second and 59 minutes")
	}
	session, err := c.source.Credentials(ctx, c.scope, ttl+30*time.Second)
	if err != nil {
		return nil, err
	}
	return newS3(c.config, c.endpoint, credentials.NewStaticV4(session.AccessKeyID, session.SecretAccessKey, session.SessionToken))
}

func (c *stsClient) PresignedGetObject(ctx context.Context, bucket, key string, ttl time.Duration, query url.Values) (*url.URL, error) {
	client, err := c.presigner(ctx, ttl)
	if err != nil {
		return nil, err
	}
	return client.PresignedGetObject(ctx, bucket, key, ttl, query)
}

func (c *stsClient) PresignedPutObject(ctx context.Context, bucket, key string, ttl time.Duration) (*url.URL, error) {
	client, err := c.presigner(ctx, ttl)
	if err != nil {
		return nil, err
	}
	return client.PresignedPutObject(ctx, bucket, key, ttl)
}
