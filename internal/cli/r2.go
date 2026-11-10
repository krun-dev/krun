package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/securetoken"
)

const r2Help = `Usage: krun r2 bucket|object COMMAND [--org ID]
  bucket list|info|create|update
  object list|head|put|get|delete|url

Bucket creation takes NAME. Bucket info and update take BUCKET.
Object head, put, get, delete and url take BUCKET/KEY.
`

func (c *Client) r2(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "--help" {
		_, err := fmt.Fprint(c.Out, r2Help)
		return err
	}
	if len(args) == 1 || args[1] == "--help" {
		_, err := fmt.Fprint(c.Out, r2Help)
		return err
	}
	group, action, rest := args[0], args[1], args[2:]
	switch group {
	case "bucket":
		return c.r2Bucket(ctx, action, rest)
	case "object":
		return c.r2Object(ctx, action, rest)
	default:
		return fmt.Errorf("unknown R2 group %q; use krun r2 --help", group)
	}
}

func (c *Client) r2Bucket(ctx context.Context, action string, args []string) error {
	f := flags("r2 bucket "+action, c.Err)
	org := c.orgFlag(f)
	var value, operandName string
	var enabled bool
	var revision int64
	switch action {
	case "list":
	case "create":
		operandName = "NAME"
	case "info", "update":
		operandName = "BUCKET"
		if action == "update" {
			f.BoolVar(&enabled, "enabled", true, "Enable or disable the logical bucket")
			f.Int64Var(&revision, "revision", -1, "Expected bucket revision (default: read it)")
		}
	default:
		return fmt.Errorf("unknown R2 bucket command %q", action)
	}
	if operandName != "" {
		value, args = operand(args)
		f.Usage = func() {
			fmt.Fprintf(c.Err, "Usage: krun r2 bucket %s %s [options]\n", action, operandName)
			f.PrintDefaults()
		}
	}
	if err := parseFlags(f, args); err != nil {
		return err
	}
	base, err := organizationPath(*org)
	if err != nil {
		return err
	}
	path := base + "/buckets"
	if action == "list" {
		return c.call(ctx, "GET", path, nil)
	}
	if err := requireOperand(value, operandName); err != nil {
		return err
	}
	if action == "create" {
		return c.call(ctx, "POST", path, map[string]string{"name": value})
	}
	id, err := c.r2BucketID(ctx, path, value)
	if err != nil {
		return err
	}
	path += "/" + url.PathEscape(id)
	if action == "info" {
		return c.call(ctx, "GET", path, nil)
	}
	if !flagSet(f, "enabled") {
		return errors.New("specify --enabled=true or --enabled=false")
	}
	if revision < 0 {
		var current domain.WorkerBucket
		if err := c.request(ctx, "GET", path, nil, &current, false); err != nil {
			return err
		}
		revision = current.Revision
	}
	return c.call(ctx, "PATCH", path, map[string]any{"enabled": enabled, "revision": revision})
}

func (c *Client) r2Object(ctx context.Context, action string, args []string) error {
	f := flags("r2 object "+action, c.Err)
	org := c.orgFlag(f)
	var bucket, key, prefix, cursor, file, reference string
	metadata := map[string]*string{}
	var limit int
	var force, pipe bool
	if action == "list" {
		f.StringVar(&bucket, "bucket", "", "Bucket name or ID")
		f.StringVar(&prefix, "prefix", "", "Object key prefix")
		f.StringVar(&cursor, "cursor", "", "Pagination cursor")
		f.IntVar(&limit, "limit", 100, "Page size (1–1000)")
	} else {
		reference, args = operand(args)
		f.Usage = func() {
			fmt.Fprintf(c.Err, "Usage: krun r2 object %s BUCKET/KEY [options]\n", action)
			f.PrintDefaults()
		}
	}
	switch action {
	case "put":
		f.StringVar(&file, "file", "", "Local file to upload")
		f.BoolVar(&pipe, "pipe", false, "Read the object from stdin")
		metadata["contentType"] = f.String("content-type", "", "Object content type")
		metadata["contentDisposition"] = f.String("content-disposition", "", "Object content disposition")
		metadata["contentEncoding"] = f.String("content-encoding", "", "Object content encoding")
		metadata["contentLanguage"] = f.String("content-language", "", "Object content language")
		metadata["cacheControl"] = f.String("cache-control", "", "Object cache control")
		metadata["cacheExpiry"] = f.String("expires", "", "Object cache expiry in RFC 3339 format")
	case "get":
		f.StringVar(&file, "file", "", "Local destination")
		f.BoolVar(&pipe, "pipe", false, "Write the object to stdout")
		f.BoolVar(&force, "force", false, "Replace an existing destination file")
	case "list", "head", "delete", "url":
	default:
		return fmt.Errorf("unknown R2 object command %q", action)
	}
	if err := parseFlags(f, args); err != nil {
		return err
	}
	base, err := organizationPath(*org)
	if err != nil {
		return err
	}
	query := url.Values{}
	if action == "list" {
		if bucket == "" || limit < 1 || limit > 1000 {
			return errors.New("--bucket is required and --limit must be between 1 and 1000")
		}
		query.Set("prefix", prefix)
		query.Set("cursor", cursor)
		query.Set("limit", strconv.Itoa(limit))
	} else {
		bucket, key, _ = strings.Cut(reference, "/")
		if bucket == "" || key == "" {
			return errors.New("BUCKET/KEY is required before the options")
		}
		query.Set("key", key)
	}
	if (action == "put" || action == "get") && (file != "") == pipe {
		return errors.New("choose exactly one of --file or --pipe")
	}
	if pipe && force {
		return errors.New("--force requires --file")
	}
	buckets := base + "/buckets"
	id, err := c.r2BucketID(ctx, buckets, bucket)
	if err != nil {
		return err
	}
	path := buckets + "/" + url.PathEscape(id) + "/objects"
	switch action {
	case "list":
		return c.call(ctx, "GET", path+"?"+query.Encode(), nil)
	case "head":
		return c.call(ctx, "GET", path+"/head?"+query.Encode(), nil)
	case "delete":
		return c.call(ctx, "DELETE", path+"?"+query.Encode(), nil)
	case "url":
		return c.call(ctx, "POST", path+"/access", domain.BucketObjectAccessInput{Key: key, Operation: "download"})
	case "put":
		headers := map[string]string{}
		for name, value := range metadata {
			if *value != "" {
				headers[name] = *value
			}
		}
		return c.r2Put(ctx, path, key, file, headers)
	default:
		return c.r2Get(ctx, path, key, file, force)
	}
}

func (c *Client) r2BucketID(ctx context.Context, path, bucket string) (string, error) {
	// Native bucket routes also resolve names that have the new ID shape.
	if securetoken.IsID(bucket, "bkt") || strings.HasPrefix(bucket, "bkt_") {
		return bucket, nil
	}
	var result struct {
		Items []domain.WorkerBucket `json:"items"`
	}
	if err := c.request(ctx, "GET", path, nil, &result, false); err != nil {
		return "", fmt.Errorf("resolve bucket name %q (use its ID to skip lookup): %w", bucket, err)
	}
	for _, item := range result.Items {
		if item.Name == bucket {
			return item.ID, nil
		}
	}
	return "", fmt.Errorf("bucket %q was not found", bucket)
}

func (c *Client) r2Put(ctx context.Context, path, key, filename string, metadata map[string]string) error {
	const maxSize int64 = 5 << 30
	var file *os.File
	var size int64
	var err error
	if filename != "" {
		file, size, err = openRegularFile(filename)
		if err != nil {
			return err
		}
		defer file.Close()
	} else {
		file, err = os.CreateTemp("", ".krun-r2-upload-*")
		if err != nil {
			return err
		}
		defer os.Remove(file.Name())
		defer file.Close()
		// The storage signature binds the request to the exact content length.
		size, err = io.Copy(file, io.LimitReader(c.In, maxSize+1))
		if err != nil {
			return err
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return err
		}
	}
	if size > maxSize {
		return errors.New("object upload exceeds the 5 GiB limit")
	}
	input := domain.BucketObjectAccessInput{Key: key, Operation: "upload", Size: size, HTTPMetadata: metadata}
	var grant domain.BucketObjectAccess
	if err := c.request(ctx, "POST", path+"/access", input, &grant, false); err != nil {
		return err
	}
	if grant.Method != http.MethodPut {
		return errors.New("invalid object upload method")
	}
	var source io.Reader = file
	if size == 0 {
		source = http.NoBody
	}
	resp, err := transfer(ctx, http.MethodPut, grant.URL, grant.Headers, source, size)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return c.outputJSON(map[string]any{"key": key, "size": size})
}

func (c *Client) r2Get(ctx context.Context, path, key, filename string, replace bool) error {
	input := domain.BucketObjectAccessInput{Key: key, Operation: "download"}
	var grant domain.BucketObjectAccess
	if err := c.request(ctx, "POST", path+"/access", input, &grant, false); err != nil {
		return err
	}
	if grant.Method != http.MethodGet {
		return errors.New("invalid object download method")
	}
	resp, err := transfer(ctx, http.MethodGet, grant.URL, grant.Headers, nil, 0)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if filename == "" {
		_, err := io.Copy(c.Out, resp.Body)
		return err
	}
	return writeDownload(filename, resp.Body, replace)
}
