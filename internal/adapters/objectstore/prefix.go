package objectstore

import (
	"context"
	"errors"
	"io"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
)

// Callers and persisted inventories use logical keys. Every request, including
// signed URLs and bulk deletes, stays inside this installation's prefix.
type prefixedClient struct {
	Client
	prefix string
}

func (c *prefixedClient) key(key string, empty bool) (string, error) {
	clean := strings.TrimSuffix(key, "/")
	if empty && key == "" {
		return c.prefix, nil
	}
	hasControlCharacters := strings.IndexFunc(clean, func(r rune) bool {
		return r < 32 || r == 127
	}) >= 0
	invalidPath := clean == "" || clean == "." || clean == ".." ||
		path.IsAbs(clean) || path.Clean(clean) != clean ||
		strings.HasPrefix(clean, "../") || strings.Contains(clean, "\\")
	if invalidPath || hasControlCharacters {
		return "", errors.New("invalid logical object key")
	}
	return c.prefix + key, nil
}

func (c *prefixedClient) GetObject(
	ctx context.Context,
	bucket, key string,
	opts GetObjectOptions,
) (*minio.Object, error) {
	qualified, err := c.key(key, false)
	if err != nil {
		return nil, err
	}
	return c.Client.GetObject(ctx, bucket, qualified, opts)
}

func (c *prefixedClient) StatObject(
	ctx context.Context,
	bucket, key string,
	opts StatObjectOptions,
) (ObjectInfo, error) {
	qualified, err := c.key(key, false)
	if err != nil {
		return ObjectInfo{}, err
	}
	info, err := c.Client.StatObject(ctx, bucket, qualified, opts)
	info.Key = strings.TrimPrefix(info.Key, c.prefix)
	return info, err
}

func (c *prefixedClient) PutObject(
	ctx context.Context,
	bucket, key string,
	r io.Reader,
	size int64,
	opts PutObjectOptions,
) (UploadInfo, error) {
	qualified, err := c.key(key, false)
	if err != nil {
		return UploadInfo{}, err
	}
	info, err := c.Client.PutObject(ctx, bucket, qualified, r, size, opts)
	info.Key = strings.TrimPrefix(info.Key, c.prefix)
	return info, err
}

func (c *prefixedClient) RemoveObject(ctx context.Context, bucket, key string, opts RemoveObjectOptions) error {
	qualified, err := c.key(key, false)
	if err != nil {
		return err
	}
	return c.Client.RemoveObject(ctx, bucket, qualified, opts)
}

func (c *prefixedClient) PresignedPutObject(
	ctx context.Context,
	bucket, key string,
	ttl time.Duration,
) (*url.URL, error) {
	qualified, err := c.key(key, false)
	if err != nil {
		return nil, err
	}
	return c.Client.PresignedPutObject(ctx, bucket, qualified, ttl)
}

func (c *prefixedClient) PresignedGetObject(
	ctx context.Context,
	bucket, key string,
	ttl time.Duration,
	q url.Values,
) (*url.URL, error) {
	qualified, err := c.key(key, false)
	if err != nil {
		return nil, err
	}
	return c.Client.PresignedGetObject(ctx, bucket, qualified, ttl, q)
}

func (c *prefixedClient) ListObjects(ctx context.Context, bucket string, opts ListObjectsOptions) <-chan ObjectInfo {
	out := make(chan ObjectInfo)
	go func() {
		defer close(out)
		var err error
		opts.Prefix, err = c.key(opts.Prefix, true)
		if err == nil && opts.StartAfter != "" {
			opts.StartAfter, err = c.key(opts.StartAfter, false)
		}
		if err != nil {
			select {
			case out <- ObjectInfo{Err: err}:
			case <-ctx.Done():
			}
			return
		}
		for info := range c.Client.ListObjects(ctx, bucket, opts) {
			if info.Err == nil && !strings.HasPrefix(info.Key, c.prefix) {
				continue
			}
			info.Key = strings.TrimPrefix(info.Key, c.prefix)
			select {
			case out <- info:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}

func (c *prefixedClient) RemoveObjects(
	ctx context.Context,
	bucket string,
	objects <-chan ObjectInfo,
	opts RemoveObjectsOptions,
) <-chan minio.RemoveObjectError {
	ctx, cancel := context.WithCancel(ctx)
	input := make(chan ObjectInfo)
	invalid := make(chan minio.RemoveObjectError)
	out := make(chan minio.RemoveObjectError)
	go func() {
		defer close(input)
		defer close(invalid)
		for {
			select {
			case info, ok := <-objects:
				if !ok {
					return
				}
				key, err := c.key(info.Key, false)
				if err != nil {
					select {
					case invalid <- minio.RemoveObjectError{ObjectName: info.Key, Err: err}:
					case <-ctx.Done():
						return
					}
					continue
				}
				info.Key = key
				select {
				case input <- info:
				case <-ctx.Done():
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		defer close(out)
		defer cancel()
		remote := c.Client.RemoveObjects(ctx, bucket, input, opts)
		for remote != nil || invalid != nil {
			var item minio.RemoveObjectError
			select {
			case value, ok := <-remote:
				if !ok {
					remote = nil
					continue
				}
				item = value
				item.ObjectName = strings.TrimPrefix(item.ObjectName, c.prefix)
			case value, ok := <-invalid:
				if !ok {
					invalid = nil
					continue
				}
				item = value
			case <-ctx.Done():
				return
			}
			select {
			case out <- item:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}
