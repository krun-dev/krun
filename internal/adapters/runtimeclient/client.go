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
	return call(ctx, client, pool, podIP, baseOverride, path, payload, "")
}

// SessionIdentity verifies a stream proof without exposing the Pod's current proof.
func SessionIdentity(ctx context.Context, client Doer, pool appconfig.WorkerRuntimePool, podIP, proof string) (map[string]any, int, error) {
	return call(ctx, client, pool, podIP, "", "/config-session", nil, proof)
}

func call(ctx context.Context, client Doer, pool appconfig.WorkerRuntimePool, podIP, baseOverride, path string, payload any, proof string) (map[string]any, int, error) {
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
	if proof != "" {
		req.Header.Set("X-Krun-Session-Proof", proof)
	}
	resp, e := client.Do(req)
	if e != nil {
		return nil, 0, e
	}
	defer resp.Body.Close()
	var result map[string]any
	e = json.NewDecoder(io.LimitReader(resp.Body, 65536)).Decode(&result)
	return result, resp.StatusCode, e
}
