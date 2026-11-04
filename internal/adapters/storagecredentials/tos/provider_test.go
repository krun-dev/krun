package tos

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/krun-dev/krun/internal/adapters/storagecredentials/provider"
	"github.com/krun-dev/krun/internal/domain"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func options() Options {
	return Options{AccessKeyID: "example-access", SecretAccessKey: "example-secret", RoleTrn: "trn:iam::1000000000:role/example", Region: "cn-beijing"}
}

func scope() domain.StorageCredentialScope {
	return domain.StorageCredentialScope{
		Organization:          "example-org",
		BucketID:              "bucket-id",
		StoreID:               "store-id",
		Bucket:                "example-bucket",
		Prefix:                "worker-data/org/example/bucket/",
		Access:                domain.StorageRead | domain.StorageWrite | domain.StorageDelete | domain.StorageList,
		AuthorizationRevision: 1,
	}
}

func TestTOSRequestAndResponse(t *testing.T) {
	p, err := New(options())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	p.now = func() time.Time { return now }
	p.client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != "POST" || r.URL.Host != "sts.volcengineapi.com" || r.URL.Query().Get("Action") != "AssumeRole" {
			t.Fatal("incorrect STS target")
		}
		if strings.Contains(r.URL.String(), "RoleTrn") || strings.Contains(r.URL.String(), "Policy") {
			t.Fatal("session details in URL")
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "HMAC-SHA256 Credential=example-access/20260101/cn-beijing/sts/request,") {
			t.Fatal("incorrect OpenAPI signature")
		}
		raw, _ := io.ReadAll(r.Body)
		values, err := url.ParseQuery(string(raw))
		if err != nil || values.Get("RoleTrn") != options().RoleTrn || values.Get("DurationSeconds") != "3600" {
			t.Fatal("incorrect role or duration")
		}
		var policy struct{ Statement []statement }
		if json.Unmarshal([]byte(values.Get("Policy")), &policy) != nil || len(policy.Statement) != 2 {
			t.Fatal("invalid policy")
		}
		if policy.Statement[0].Resource[0] != "trn:tos:::example-bucket/worker-data/org/example/bucket/*" ||
			policy.Statement[1].Condition["StringLike"]["tos:prefix"][0] != scope().Prefix+"*" {
			t.Fatal("policy is not prefix scoped")
		}
		body := `{"Result":{"Credentials":{"AccessKeyId":"temp-access","SecretAccessKey":"temp-secret","SessionToken":"temp-token","ExpiredTime":"2026-01-01T01:00:00Z"}}}`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})
	c, err := p.Retrieve(context.Background(), scope(), time.Hour)
	if err != nil || c.SessionToken != "temp-token" || !c.ExpiresAt.Equal(now.Add(time.Hour)) {
		t.Fatal("invalid returned credentials", err)
	}
}

func TestTOSSafeBusinessErrorsAndInvalidResponses(t *testing.T) {
	for _, test := range []struct {
		code   string
		status int
		kind   provider.ErrorKind
	}{
		{"AccessDenied", 200, provider.PermissionDenied},
		{"SignatureDoesNotMatch", 200, provider.AuthenticationFailed},
		{"LimitExceeded", 200, provider.Throttled},
		{"InvalidParameter", 200, provider.InvalidConfiguration},
		{"", 503, provider.Unavailable},
	} {
		t.Run(fmt.Sprintf("%s/%d", test.code, test.status), func(t *testing.T) {
			p, _ := New(options())
			p.client.Transport = transportFunc(func(*http.Request) (*http.Response, error) {
				raw, _ := json.Marshal(map[string]any{"ResponseMetadata": map[string]any{"RequestId": "safe-request-id", "Error": map[string]string{"Code": test.code, "Message": "PRIVATE SIGNATURE AND SECRET"}}})
				return &http.Response{StatusCode: test.status, Body: io.NopCloser(strings.NewReader(string(raw))), Header: make(http.Header)}, nil
			})
			_, err := p.Retrieve(context.Background(), scope(), time.Hour)
			var issue *provider.Error
			if !errors.As(err, &issue) || issue.Kind != test.kind || strings.Contains(err.Error(), "PRIVATE") {
				t.Fatalf("incorrect safe error: %v", err)
			}
		})
	}
	for _, body := range []string{"not json", `{}`, `{"Result":{"Credentials":{"AccessKeyId":"access","SecretAccessKey":"secret","SessionToken":"token","ExpiredTime":"2000-01-01T00:00:00Z"}}}`, strings.Repeat("x", 65537)} {
		p, _ := New(options())
		p.client.Transport = transportFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		})
		if _, err := p.Retrieve(context.Background(), scope(), time.Hour); err == nil {
			t.Fatal("accepted malformed credentials")
		}
	}
}

func TestTOSRejectsScopeInjectionBeforeNetwork(t *testing.T) {
	for _, prefix := range []string{"", "/root/", "root/*/", "root/?/", "root/../", "root/./", "root//", "root/\n/", "root"} {
		p, _ := New(options())
		p.client.Transport = transportFunc(func(*http.Request) (*http.Response, error) { t.Fatal("invalid scope reached network"); return nil, nil })
		s := scope()
		s.Prefix = prefix
		if _, err := p.Retrieve(context.Background(), s, time.Hour); err == nil {
			t.Fatalf("accepted prefix %q", prefix)
		}
	}
	for _, endpoint := range []string{"https://attacker.example", "https://sts.volcengineapi.com@attacker.example", "http://sts.volcengineapi.com", "https://sts.volcengineapi.com/extra"} {
		o := options()
		o.Endpoint = endpoint
		if _, err := New(o); err == nil {
			t.Fatal("accepted non-STS target")
		}
	}
}
