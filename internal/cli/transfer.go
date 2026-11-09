package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

// Presigned requests use a separate client and carry only the signed headers.
func transfer(ctx context.Context, method, target string, headers map[string]string, body io.Reader, size int64) (*http.Response, error) {
	u, err := url.Parse(target)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Fragment != "" ||
		(u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1"))) {
		return nil, errors.New("invalid storage transfer URL")
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, errors.New("invalid storage transfer request")
	}
	req.ContentLength = size
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	if method == http.MethodGet && req.Header.Get("Accept-Encoding") == "" {
		// Object metadata can specify gzip. Preserve the stored bytes.
		req.Header.Set("Accept-Encoding", "identity")
	}
	client := &http.Client{
		Timeout:       30 * time.Minute,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("storage transfer failed")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		return nil, fmt.Errorf("storage transfer returned HTTP %d", resp.StatusCode)
	}
	return resp, nil
}

func openRegularFile(path string) (*os.File, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, 0, err
	}
	if !info.Mode().IsRegular() {
		file.Close()
		return nil, 0, errors.New("input must be a regular file")
	}
	return file, info.Size(), nil
}

func writeDownload(path string, body io.Reader, replace bool) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".krun-download-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	_, copyErr := io.Copy(temp, body)
	closeErr := temp.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		return err
	}
	if replace {
		return os.Rename(temp.Name(), path)
	}
	return os.Link(temp.Name(), path)
}
