// Package tos obtains prefix-scoped credentials from Volcengine STS. Object
// traffic uses the separate S3 protocol and never passes through this provider.
package tos

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/krun-dev/krun/internal/adapters/storagecredentials/provider"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/securetoken"
)

type Options struct {
	AccessKeyID     string
	SecretAccessKey string
	RoleTrn         string
	Region          string
	Endpoint        string
}

type Provider struct {
	options Options
	client  *http.Client
	now     func() time.Time
}

var rolePattern = regexp.MustCompile(`^trn:iam::[0-9]+:role/[A-Za-z0-9+=,.@_-]{1,64}$`)
var regionPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

func New(options Options) (*Provider, error) {
	if options.Endpoint == "" {
		options.Endpoint = "https://sts.volcengineapi.com"
	}
	u, err := url.Parse(options.Endpoint)
	if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		u.Path != "" || u.Port() != "" ||
		(u.Host != "sts.volcengineapi.com" && u.Host != "sts."+options.Region+".volcengineapi.com") ||
		!rolePattern.MatchString(options.RoleTrn) || !regionPattern.MatchString(options.Region) ||
		options.AccessKeyID == "" || options.SecretAccessKey == "" {
		return nil, &provider.Error{Kind: provider.InvalidConfiguration}
	}
	return &Provider{
		options: options,
		client:  &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		now:     time.Now,
	}, nil
}

func (*Provider) Capabilities() provider.Capabilities {
	return provider.Capabilities{CanIssueTemporary: true, CanRestrictScope: true}
}

func (p *Provider) Retrieve(ctx context.Context, scope domain.StorageCredentialScope, requestedTTL time.Duration) (domain.StorageCredentials, error) {
	policy, err := sessionPolicy(scope)
	if err != nil {
		return domain.StorageCredentials{}, err
	}
	if requestedTTL < 900*time.Second || requestedTTL > 12*time.Hour || requestedTTL%time.Second != 0 {
		return domain.StorageCredentials{}, &provider.Error{Kind: provider.InvalidConfiguration}
	}
	parameters := url.Values{
		"RoleTrn":         {p.options.RoleTrn},
		"RoleSessionName": {securetoken.NewID("krun")},
		"DurationSeconds": {strconv.FormatInt(int64(requestedTTL/time.Second), 10)},
		"Policy":          {policy},
	}
	u := p.options.Endpoint + "/?Action=AssumeRole&Version=2018-01-01"
	body := parameters.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(body))
	if err != nil {
		return domain.StorageCredentials{}, &provider.Error{Kind: provider.InvalidConfiguration}
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	sign(req, body, p.options, p.now().UTC())
	resp, err := p.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return domain.StorageCredentials{}, ctx.Err()
		}
		return domain.StorageCredentials{}, &provider.Error{Kind: provider.Unavailable}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 65537))
	if err != nil || len(raw) > 65536 {
		return domain.StorageCredentials{}, &provider.Error{Kind: provider.Unavailable}
	}
	var response struct {
		ResponseMetadata struct {
			RequestID string `json:"RequestId"`
			Error     *struct {
				Code string `json:"Code"`
			} `json:"Error"`
		} `json:"ResponseMetadata"`
		Result struct {
			Credentials struct {
				AccessKeyID     string    `json:"AccessKeyId"`
				SecretAccessKey string    `json:"SecretAccessKey"`
				SessionToken    string    `json:"SessionToken"`
				ExpiredTime     time.Time `json:"ExpiredTime"`
			} `json:"Credentials"`
		} `json:"Result"`
	}
	decodeErr := json.Unmarshal(raw, &response)
	requestID := safeRequestID(response.ResponseMetadata.RequestID)
	if response.ResponseMetadata.Error != nil {
		return domain.StorageCredentials{}, &provider.Error{Kind: errorKind(response.ResponseMetadata.Error.Code, resp.StatusCode), RequestID: requestID}
	}
	if resp.StatusCode != http.StatusOK || decodeErr != nil {
		return domain.StorageCredentials{}, &provider.Error{Kind: errorKind("", resp.StatusCode), RequestID: requestID}
	}
	c := response.Result.Credentials
	if c.AccessKeyID == "" || c.SecretAccessKey == "" || c.SessionToken == "" || !c.ExpiredTime.After(p.now()) {
		return domain.StorageCredentials{}, &provider.Error{Kind: provider.Unavailable, RequestID: requestID}
	}
	return domain.StorageCredentials{
		AccessKeyID: c.AccessKeyID, SecretAccessKey: c.SecretAccessKey,
		SessionToken: c.SessionToken, ExpiresAt: c.ExpiredTime,
	}, nil
}

func safeRequestID(value string) string {
	if len(value) > 128 || strings.IndexFunc(value, func(r rune) bool { return r < 33 || r > 126 }) >= 0 {
		return ""
	}
	return value
}

func errorKind(code string, status int) provider.ErrorKind {
	if status == 429 || strings.Contains(code, "LimitExceeded") || strings.Contains(code, "Throttl") {
		return provider.Throttled
	}
	if status == 401 || strings.Contains(code, "Signature") || strings.Contains(code, "AccessKey") || strings.Contains(code, "Token") {
		return provider.AuthenticationFailed
	}
	if status == 403 || strings.Contains(code, "AccessDenied") || strings.Contains(code, "Unauthorized") {
		return provider.PermissionDenied
	}
	if status == 400 || strings.Contains(code, "Invalid") || strings.Contains(code, "Missing") {
		return provider.InvalidConfiguration
	}
	return provider.Unavailable
}
