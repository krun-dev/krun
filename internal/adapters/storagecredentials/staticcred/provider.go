package staticcred

import (
	"context"
	"time"

	"github.com/krun-dev/krun/internal/adapters/storagecredentials/provider"
	"github.com/krun-dev/krun/internal/domain"
)

type Provider struct{ credentials domain.StorageCredentials }

func New(accessKey, secretKey string) (*Provider, error) {
	if accessKey == "" || secretKey == "" {
		return nil, &provider.Error{Kind: provider.InvalidConfiguration}
	}
	return &Provider{credentials: domain.StorageCredentials{AccessKeyID: accessKey, SecretAccessKey: secretKey}}, nil
}

func (*Provider) Capabilities() provider.Capabilities { return provider.Capabilities{} }

func (p *Provider) Retrieve(ctx context.Context, scope domain.StorageCredentialScope, _ time.Duration) (domain.StorageCredentials, error) {
	if err := ctx.Err(); err != nil {
		return domain.StorageCredentials{}, err
	}
	if err := provider.ValidateScope(scope); err != nil {
		return domain.StorageCredentials{}, err
	}
	return p.credentials, nil
}
