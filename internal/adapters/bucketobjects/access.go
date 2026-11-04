package bucketobjects

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/krun-dev/krun/internal/adapters/objectdelivery"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/securetoken"
)

type envelope struct {
	Version        string            `json:"version"`
	CustomMetadata map[string]string `json:"customMetadata"`
	HTTPMetadata   map[string]string `json:"httpMetadata"`
}

var httpFields = map[string]string{
	"contentType": "Content-Type", "contentLanguage": "Content-Language",
	"contentDisposition": "Content-Disposition", "contentEncoding": "Content-Encoding",
	"cacheControl": "Cache-Control", "cacheExpiry": "Expires",
}

func metadataHeaders(data envelope) (http.Header, error) {
	if data.CustomMetadata == nil {
		data.CustomMetadata = map[string]string{}
	}
	if data.HTTPMetadata == nil {
		data.HTTPMetadata = map[string]string{}
	}
	if len(data.CustomMetadata) > 64 {
		return nil, invalid("Metadata exceeds the storage profile budget")
	}
	headers := make(http.Header)
	for name, value := range data.HTTPMetadata {
		header, ok := httpFields[name]
		if !ok || len(value) > 1800 || strings.ContainsAny(value, "\r\n\x00") {
			return nil, invalid("Invalid HTTP metadata value")
		}
		if name == "cacheExpiry" {
			date, err := time.Parse(time.RFC3339Nano, value)
			if err != nil {
				return nil, invalid("Invalid cacheExpiry")
			}
			value = date.UTC().Format(http.TimeFormat)
		}
		headers.Set(header, value)
	}
	// Disable HTML escaping to match the Workers JSON envelope and its budget.
	var raw strings.Builder
	encoder := json.NewEncoder(&raw)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(data); err != nil {
		return nil, invalid("Invalid object metadata")
	}
	serialized := strings.TrimSuffix(raw.String(), "\n")
	encoded := base64.StdEncoding.EncodeToString([]byte(serialized))
	if len(serialized) > 1350 || len(encoded) > 1800 {
		return nil, invalid("Metadata exceeds the storage profile budget")
	}
	headers.Set("X-Amz-Meta-Krun-V1", encoded)
	return headers, nil
}

func (c *Client) Access(ctx context.Context, b domain.WorkerBucket, input domain.BucketObjectAccessInput) (domain.BucketObjectAccess, error) {
	result := domain.BucketObjectAccess{Key: input.Key, Method: http.MethodGet, Headers: map[string]string{}}
	if !validKey(input.Key, false) {
		return result, invalid("Invalid object key")
	}
	if input.Operation != "upload" && (len(input.HTTPMetadata) != 0 || len(input.CustomMetadata) != 0 || input.Size != 0) {
		return result, invalid("Metadata and size are only accepted for upload")
	}
	if input.Operation == "delivery" {
		store, ok := c.stores[b.Store]
		if !ok || !b.Enabled {
			return result, domain.NotFound()
		}
		provider := objectdelivery.New(store.Delivery)
		if provider == nil {
			return result, domain.Problem(409, "OBJECT_DELIVERY_NOT_CONFIGURED", "CDN delivery is not configured for this bucket")
		}
		return provider.Access(b, input.Key, time.Now().UTC())
	}
	headers := make(http.Header)
	params := make(url.Values)
	access := domain.StorageRead
	switch input.Operation {
	case "upload":
		if input.Size < 0 || input.Size > 5<<30 {
			return result, invalid("Single uploads must be at most 5 GiB")
		}
		result.Method = http.MethodPut
		access = domain.StorageWrite
		result.Version = securetoken.UUID()
		var err error
		headers, err = metadataHeaders(envelope{result.Version, input.CustomMetadata, input.HTTPMetadata})
		if err != nil {
			return result, err
		}
		// Browsers set Content-Length themselves. SigV4 binds it to this grant.
		headers.Set("Content-Length", strconv.FormatInt(input.Size, 10))
	case "download":
		params.Set("response-content-disposition", mime.FormatMediaType("attachment", map[string]string{"filename": path.Base(input.Key)}))
	case "read":
	default:
		return result, invalid("Unknown object access operation")
	}
	sdk, store, prefix, expires, err := c.connect(ctx, b, access)
	if err != nil {
		return result, err
	}
	u, err := sdk.PresignHeader(ctx, result.Method, store.Bucket, prefix+input.Key, time.Until(expires), params, headers)
	if err != nil {
		return result, storageError(err)
	}
	result.URL = u.String()
	result.ExpiresAt = expires
	for name := range headers {
		if name != "Content-Length" {
			result.Headers[name] = headers.Get(name)
		}
	}
	return result, nil
}
