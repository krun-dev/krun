// Package bucketobjects implements logical-bucket metadata operations and short
// lived data-plane capabilities. It never reads or writes object bodies.
package bucketobjects

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type CredentialSource interface {
	Credentials(context.Context, domain.StorageCredentialScope, time.Duration) (domain.StorageCredentials, error)
}

type Client struct {
	stores map[string]config.WorkerObjectStore
	source CredentialSource
	key    []byte
}

func New(c *config.WorkerObjectStorageConfig, source CredentialSource) *Client {
	client := &Client{stores: map[string]config.WorkerObjectStore{}, source: source}
	if c != nil {
		client.key = []byte(c.CursorKey)
		for _, store := range c.Stores {
			client.stores[store.ID] = store
		}
	}
	return client
}

// Core's listing API uses context.Background internally. Bind its transport to
// this operation's context as well; cancellation must also stop a slow listing.
type requestTransport struct{ ctx context.Context }

func (t requestTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return http.DefaultTransport.RoundTrip(r.Clone(t.ctx))
}

func (c *Client) connect(ctx context.Context, b domain.WorkerBucket, access domain.StorageAccess) (*minio.Core, config.WorkerObjectStore, string, time.Time, error) {
	store, ok := c.stores[b.Store]
	if !ok || !b.Enabled || b.ID == "" || b.Organization == "" || c.source == nil {
		return nil, store, "", time.Time{}, domain.NotFound()
	}
	prefix := store.RootPrefix + "/org/" + b.Organization + "/buckets/" + b.ID + "/"
	grant, err := c.source.Credentials(ctx, domain.StorageCredentialScope{
		Organization:          b.Organization,
		BucketID:              b.ID,
		StoreID:               b.Store,
		Bucket:                store.Bucket,
		Prefix:                prefix,
		Access:                access,
		AuthorizationRevision: b.Revision,
	}, 2*time.Minute)
	if err != nil {
		return nil, store, "", time.Time{}, unavailable()
	}
	u, err := url.Parse(store.Endpoint)
	if err != nil {
		return nil, store, "", time.Time{}, unavailable()
	}
	sdk, err := minio.NewCore(u.Host, &minio.Options{
		Creds:     credentials.NewStaticV4(grant.AccessKeyID, grant.SecretAccessKey, grant.SessionToken),
		Secure:    u.Scheme == "https",
		Region:    store.Region,
		Transport: requestTransport{ctx},
		BucketLookupViaURL: func(url.URL, string) minio.BucketLookupType {
			if store.AddressingStyle == "virtual" {
				return minio.BucketLookupDNS
			}
			return minio.BucketLookupPath
		},
	})
	if err != nil {
		return nil, store, "", time.Time{}, unavailable()
	}
	expires := time.Now().UTC().Add(5 * time.Minute).Truncate(time.Second)
	if !grant.ExpiresAt.IsZero() && expires.After(grant.ExpiresAt.Add(-30*time.Second)) {
		expires = grant.ExpiresAt.Add(-30 * time.Second).Truncate(time.Second)
	}
	if time.Until(expires) < time.Minute {
		return nil, store, "", time.Time{}, unavailable()
	}
	return sdk, store, prefix, expires, nil
}

func validKey(key string, allowEmpty bool) bool {
	if key == "" {
		return allowEmpty
	}
	if len(key) > 1024 || !utf8.ValidString(key) || strings.HasPrefix(key, "/") || strings.ContainsAny(key, "\\\x00\r\n") {
		return false
	}
	for _, segment := range strings.Split(key, "/") {
		// Both browsers and CDNs normalize these segments. Refuse ambiguous
		// paths even though some object stores accept them as literal keys.
		if segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

func invalid(message string) error { return domain.Problem(422, "INVALID_REQUEST", message) }
func unavailable() error {
	return domain.Problem(502, "OBJECT_STORAGE_UNAVAILABLE", "Object storage request failed")
}

func storageError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	switch minio.ToErrorResponse(err).Code {
	case "NoSuchKey", "NotFound", "NoSuchUpload":
		return domain.NotFound()
	case "PreconditionFailed":
		return domain.Problem(409, "OBJECT_CHANGED", "Object changed")
	}
	// Do not expose physical paths, cloud request bodies or credentials.
	return unavailable()
}

type pageCursor struct {
	Organization string
	Bucket       string
	Store        string
	Prefix       string
	Delimiter    string
	StartAfter   string
	Token        string
	Revision     int64
	Expires      int64
}

func (c *Client) seal(kind string, value any) (string, error) {
	if len(c.key) < 32 {
		return "", unavailable()
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, c.key)
	_, _ = mac.Write([]byte(kind + "\x00" + payload))
	return payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (c *Client) open(kind, token string, out any) error {
	if len(token) > 8192 || len(c.key) < 32 {
		return invalid("Invalid or expired object cursor")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return invalid("Invalid or expired object cursor")
	}
	mac := hmac.New(sha256.New, c.key)
	_, _ = mac.Write([]byte(kind + "\x00" + parts[0]))
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !hmac.Equal(signature, mac.Sum(nil)) {
		return invalid("Invalid or expired object cursor")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || json.Unmarshal(raw, out) != nil {
		return invalid("Invalid or expired object cursor")
	}
	return nil
}

func (c *Client) List(ctx context.Context, b domain.WorkerBucket, input domain.BucketObjectList) (domain.BucketObjectPage, error) {
	page := domain.BucketObjectPage{Objects: []domain.BucketObject{}, Prefixes: []string{}, PerPage: input.Limit}
	if !validKey(input.Prefix, true) || !validKey(input.StartAfter, true) ||
		(input.Delimiter != "/" && input.Delimiter != "") ||
		input.Limit < 1 || input.Limit > 1000 {
		return page, invalid("Invalid object prefix or page size")
	}
	token := ""
	if input.Cursor != "" {
		var cursor pageCursor
		if err := c.open("object-list", input.Cursor, &cursor); err != nil {
			return page, err
		}
		if cursor.Organization != b.Organization || cursor.Bucket != b.ID ||
			cursor.Store != b.Store || cursor.Revision != b.Revision ||
			cursor.Prefix != input.Prefix || cursor.Delimiter != input.Delimiter ||
			cursor.StartAfter != input.StartAfter || cursor.Expires <= time.Now().Unix() {
			return page, invalid("Invalid or expired object cursor")
		}
		token = cursor.Token
	}
	sdk, store, prefix, _, err := c.connect(ctx, b, domain.StorageList)
	if err != nil {
		return page, err
	}
	startAfter := ""
	if input.StartAfter != "" {
		startAfter = prefix + input.StartAfter
	}
	result, err := sdk.ListObjectsV2(store.Bucket, prefix+input.Prefix, startAfter, token, input.Delimiter, input.Limit)
	if err != nil {
		return page, storageError(err)
	}
	page.DeliveryAvailable = store.Delivery != nil
	for _, item := range result.Contents {
		if !strings.HasPrefix(item.Key, prefix+input.Prefix) {
			return page, unavailable()
		}
		page.Objects = append(page.Objects, domain.BucketObject{
			Key: strings.TrimPrefix(item.Key, prefix), Size: item.Size, ETag: item.ETag, Uploaded: item.LastModified,
		})
	}
	for _, item := range result.CommonPrefixes {
		if !strings.HasPrefix(item.Prefix, prefix+input.Prefix) {
			return page, unavailable()
		}
		page.Prefixes = append(page.Prefixes, strings.TrimPrefix(item.Prefix, prefix))
	}
	page.Truncated = result.IsTruncated
	if page.Truncated {
		if result.NextContinuationToken == "" || len(result.NextContinuationToken) > 4096 {
			return page, unavailable()
		}
		page.Cursor, err = c.seal("object-list", pageCursor{
			Organization: b.Organization,
			Bucket:       b.ID,
			Store:        b.Store,
			Revision:     b.Revision,
			Prefix:       input.Prefix,
			Delimiter:    input.Delimiter,
			StartAfter:   input.StartAfter,
			Token:        result.NextContinuationToken,
			Expires:      time.Now().Add(time.Hour).Unix(),
		})
	}
	return page, err
}

func (c *Client) Head(ctx context.Context, b domain.WorkerBucket, key string) (domain.BucketObject, error) {
	if !validKey(key, false) {
		return domain.BucketObject{}, invalid("Invalid object key")
	}
	sdk, store, prefix, _, err := c.connect(ctx, b, domain.StorageRead)
	if err != nil {
		return domain.BucketObject{}, err
	}
	info, err := sdk.StatObject(ctx, store.Bucket, prefix+key, minio.StatObjectOptions{})
	if err != nil {
		return domain.BucketObject{}, storageError(err)
	}
	compatible := false
	object := domain.BucketObject{Key: key, Size: info.Size, ETag: info.ETag, Uploaded: info.LastModified, R2Compatible: &compatible}
	var metadata envelope
	encoded := info.Metadata.Get("X-Amz-Meta-Krun-V1")
	raw, decodeErr := base64.StdEncoding.DecodeString(encoded)
	if encoded != "" && len(encoded) <= 1800 && decodeErr == nil &&
		json.Unmarshal(raw, &metadata) == nil && metadata.Version != "" &&
		metadata.CustomMetadata != nil && metadata.HTTPMetadata != nil {
		if _, err := metadataHeaders(metadata); err == nil {
			object.Version = metadata.Version
			compatible = true
			object.HTTPMetadata = metadata.HTTPMetadata
			object.CustomMetadata = metadata.CustomMetadata
		}
	}
	return object, nil
}

func (c *Client) Delete(ctx context.Context, b domain.WorkerBucket, key string) error {
	if !validKey(key, false) {
		return invalid("Invalid object key")
	}
	sdk, store, prefix, _, err := c.connect(ctx, b, domain.StorageDelete)
	if err != nil {
		return err
	}
	return storageError(sdk.RemoveObject(ctx, store.Bucket, prefix+key, minio.RemoveObjectOptions{}))
}
