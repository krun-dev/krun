package kubernetes

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	appconfig "github.com/krun-dev/krun/internal/config"
)

type Client struct {
	Base           string
	TokenFile      string
	Client         *http.Client
	Namespace      string
	NpmRegistry    string
	runtimeClass   string
	timeoutSeconds int
}

type WorkerKubeError struct {
	status    int
	operation string
}

func (e *WorkerKubeError) Error() string {
	return fmt.Sprintf("Kubernetes %s returned %d", e.operation, e.status)
}

func New(config *appconfig.WorkersConfig) (*Client, error) {
	token, e := os.ReadFile(config.KubernetesTokenFile)
	if e != nil {
		return nil, e
	}
	ca, e := os.ReadFile(config.KubernetesCAFile)
	if e != nil {
		return nil, e
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return nil, errors.New("invalid Kubernetes CA")
	}
	if strings.TrimSpace(string(token)) == "" {
		return nil, errors.New("empty Kubernetes bearer token")
	}
	return &Client{Base: config.KubernetesAPI, TokenFile: config.KubernetesTokenFile, Namespace: config.Namespace, NpmRegistry: config.NPMRegistry, runtimeClass: config.BuilderRuntimeClass, timeoutSeconds: config.BuilderTimeoutSeconds, Client: &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}}}, nil
}

func (k *Client) Call(ctx context.Context, method, route string, in, out any) error {
	var raw []byte
	if in != nil {
		var e error
		raw, e = json.Marshal(in)
		if e != nil {
			return e
		}
	}
	req, e := http.NewRequestWithContext(ctx, method, k.Base+route, bytes.NewReader(raw))
	if e != nil {
		return e
	}
	// Projected service-account tokens rotate. Re-reading this tiny local file
	// per API call follows the rotation atomically performed by kubelet and
	// avoids pinning a token that will expire during a long-running controller.
	if k.TokenFile != "" {
		token, readErr := os.ReadFile(k.TokenFile)
		if readErr != nil {
			return readErr
		}
		value := strings.TrimSpace(string(token))
		if value == "" {
			return errors.New("empty Kubernetes bearer token")
		}
		req.Header.Set("Authorization", "Bearer "+value)
	}
	req.Header.Set("Content-Type", "application/json")
	if method == http.MethodPatch {
		req.Header.Set("Content-Type", "application/merge-patch+json")
	}
	resp, e := k.Client.Do(req)
	if e != nil {
		return e
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 65536))
		return &WorkerKubeError{resp.StatusCode, method + " " + strings.Split(route, "?")[0]}
	}
	if out == nil {
		_, e = io.Copy(io.Discard, io.LimitReader(resp.Body, 2<<20))
		return e
	}
	if text, ok := out.(*string); ok {
		data, e := io.ReadAll(io.LimitReader(resp.Body, 32768))
		*text = string(data)
		return e
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out)
}

func KubeMissing(e error) bool {
	var target *WorkerKubeError
	return errors.As(e, &target) && target.status == 404
}

func KubeConflict(e error) bool {
	var target *WorkerKubeError
	return errors.As(e, &target) && target.status == 409
}

func (k *Client) Core(kind string) string {
	return "/api/v1/namespaces/" + k.Namespace + "/" + kind
}

func (k *Client) Jobs() string { return "/apis/batch/v1/namespaces/" + k.Namespace + "/jobs" }
