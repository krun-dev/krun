package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

const (
	localBuildContentMax = 32 << 20
	localBuildRequestMax = 34 << 20
	localBuildFilesMax   = 4096
)

func (b releaseBuild) resolveLocal(ctx context.Context, c *Client, base, env, buildKey string) (string, error) {
	scope, err := environmentPath(base, env)
	if err != nil {
		return "", err
	}
	payload, err := localBuildPayload(ctx, b.localPath)
	if err != nil {
		return "", fmt.Errorf("prepare --path %q: %w", b.localPath, err)
	}

	endpoint := scope + "/local-builds"
	if b.projectRoot != "" {
		query := url.Values{"project_root": {b.projectRoot}}
		endpoint += "?" + query.Encode()
	}
	payload.idempotencyKey = buildKey
	payload.timeout = 3 * time.Minute
	fmt.Fprintln(c.Err, "Build idempotency key:", buildKey)
	var result struct {
		Build struct {
			ID string `json:"id"`
		} `json:"build"`
	}
	if err := c.request(ctx, http.MethodPost, endpoint, payload, &result, false); err != nil {
		return "", err
	}
	return b.wait(ctx, c, base, result.Build.ID, "local")
}

func localBuildPayload(ctx context.Context, source string) (requestPayload, error) {
	source = filepath.Clean(source)
	info, err := os.Lstat(source)
	if err != nil {
		return requestPayload{}, err
	}
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	remaining := int64(localBuildContentMax)
	if info.IsDir() {
		if err := writeLocalBuildDirectory(ctx, source, form, &body, &remaining); err != nil {
			return requestPayload{}, err
		}
	} else {
		if !info.Mode().IsRegular() || !strings.EqualFold(filepath.Ext(source), ".zip") {
			return requestPayload{}, errors.New("--path must be a directory or a .zip file")
		}
		file, err := os.Open(source)
		if err != nil {
			return requestPayload{}, err
		}
		if err := writeLocalBuildPart(form, file, "archive", filepath.Base(source), &remaining); err != nil {
			return requestPayload{}, err
		}
	}
	if err := form.Close(); err != nil {
		return requestPayload{}, err
	}
	if body.Len() > localBuildRequestMax {
		return requestPayload{}, errors.New("multipart upload exceeds 34 MiB")
	}
	// Keep the serialized body available for OAuth refresh and request replay.
	return requestPayload{data: body.Bytes(), contentType: form.FormDataContentType()}, nil
}

func writeLocalBuildDirectory(ctx context.Context, source string, form *multipart.Writer, body *bytes.Buffer, remaining *int64) error {
	root, err := os.OpenRoot(source)
	if err != nil {
		return err
	}
	defer root.Close()

	var count int
	err = fs.WalkDir(root.FS(), ".", func(relative string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if relative != "." {
			switch entry.Name() {
			case ".git", ".wrangler", "node_modules":
				if entry.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		count++
		if count > localBuildFilesMax {
			return fmt.Errorf("local upload exceeds %d files", localBuildFilesMax)
		}

		file, err := root.Open(relative)
		if err != nil {
			return err
		}
		if err := writeLocalBuildPart(form, file, "file:"+relative, path.Base(relative), remaining); err != nil {
			return err
		}
		if body.Len() > localBuildRequestMax {
			return errors.New("multipart upload exceeds 34 MiB")
		}
		return nil
	})
	if err != nil {
		return err
	}
	if count == 0 {
		return errors.New("--path contains no files to upload")
	}
	return nil
}

func writeLocalBuildPart(form *multipart.Writer, file *os.File, field, filename string, remaining *int64) error {
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%q must be a regular file", file.Name())
	}
	if info.Size() > *remaining {
		return errors.New("local upload exceeds 32 MiB")
	}

	if strings.ContainsAny(field, "\r\n") || strings.ContainsAny(filename, "\r\n") {
		return fmt.Errorf("upload path %q contains line breaks", file.Name())
	}
	part, err := form.CreateFormFile(field, filename)
	if err != nil {
		return err
	}
	copied, err := io.Copy(part, io.LimitReader(file, *remaining+1))
	if err != nil {
		return err
	}
	if copied > *remaining {
		return errors.New("local upload exceeds 32 MiB")
	}
	*remaining -= copied
	return nil
}
