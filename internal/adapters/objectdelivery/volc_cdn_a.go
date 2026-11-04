// Package objectdelivery provides signed public-facing URLs for a logical object.
// Data-plane origin credentials and traffic remain between the CDN and storage.
package objectdelivery

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/securetoken"
)

type Provider interface {
	Access(domain.WorkerBucket, string, time.Time) (domain.BucketObjectAccess, error)
}

type volcTypeA struct {
	config *config.WorkerObjectDeliveryConfig
}

func New(c *config.WorkerObjectDeliveryConfig) Provider {
	if c == nil || c.Type != "volc_cdn_a" {
		return nil
	}
	copy := *c
	return volcTypeA{config: &copy}
}

// deliveryAccess is the Volcengine CDN Type A SHA256 protocol. The CDN must
// strip sign before origin access and verify it before serving cached content.
func (p volcTypeA) Access(b domain.WorkerBucket, key string, now time.Time) (domain.BucketObjectAccess, error) {
	c := p.config
	result := domain.BucketObjectAccess{Key: key, Method: http.MethodGet, Headers: map[string]string{}}
	if c == nil {
		return result, domain.Problem(409, "OBJECT_DELIVERY_NOT_CONFIGURED", "CDN delivery is not configured for this bucket")
	}
	if strings.Contains(key, "//") {
		return result, domain.Problem(422, "INVALID_REQUEST", "CDN paths cannot contain empty segments")
	}
	u, err := url.Parse(c.BaseURL)
	if err != nil || c.Type != "volc_cdn_a" {
		return result, domain.Problem(503, "OBJECT_DELIVERY_NOT_CONFIGURED", "CDN delivery is not configured for this bucket")
	}
	u.Path = "/org/" + b.Organization + "/buckets/" + b.ID + "/" + key
	stamp := strconv.FormatInt(now.Unix(), 10)
	rand := strings.ReplaceAll(securetoken.UUID(), "-", "")
	payload := fmt.Sprintf("%s-%s-%s-0-%s", u.EscapedPath(), stamp, rand, c.SigningKey)
	digest := sha256.Sum256([]byte(payload))
	query := make(url.Values)
	query.Set("sign", stamp+"-"+rand+"-0-"+hex.EncodeToString(digest[:]))
	u.RawQuery = query.Encode()
	result.URL = u.String()
	result.ExpiresAt = time.Unix(now.Unix()+int64(c.TTLSeconds), 0).UTC()
	return result, nil
}
