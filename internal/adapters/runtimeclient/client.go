package runtimeclient

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"time"

	appconfig "github.com/krun-dev/krun/internal/config"
)

type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

func New() Doer { return &http.Client{Timeout: 8 * time.Second} }

func Call(ctx context.Context, client Doer, pool appconfig.WorkerRuntimePool, podIP, baseOverride, path string, payload any) (map[string]any, int, error) {
	raw, _ := json.Marshal(payload)

	base := "http://" + net.JoinHostPort(podIP, "8083")
	if baseOverride != "" {
		base = baseOverride
	}
	req, e := http.NewRequestWithContext(ctx, "POST", base+path, bytes.NewReader(raw))
	if e != nil {
		return nil, 0, e
	}
	req.Header.Set("Authorization", "Bearer "+pool.ManagementToken)
	req.Header.Set("Content-Type", "application/json")
	resp, e := client.Do(req)
	if e != nil {
		return nil, 0, e
	}
	defer resp.Body.Close()
	var result map[string]any
	e = json.NewDecoder(io.LimitReader(resp.Body, 65536)).Decode(&result)
	return result, resp.StatusCode, e
}
