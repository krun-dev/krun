package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestBuildLogsFollowUsesHeaderRatherThanBuildState(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("X-Log-Poll-Interval", "1")
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"state": "succeeded", "download_url": ""})
	}))
	defer server.Close()
	var out bytes.Buffer
	c := Client{Profile: Profile{Endpoint: server.URL}, Out: &out, Err: &out}
	if err := c.buildLogs(context.Background(), []string{"logs", "--org", "o", "--worker", "s", "--build", "b", "--follow"}); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("follow made %d calls", calls)
	}
}

func TestBuildLogsFollowCanBeCancelledWhileWaiting(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Log-Poll-Interval", "10")
		_, _ = w.Write([]byte(`{"download_url":""}`))
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	var out bytes.Buffer
	c := Client{Profile: Profile{Endpoint: server.URL}, Out: &out, Err: &out}
	if err := c.buildLogs(ctx, []string{"logs", "--org", "o", "--worker", "s", "--build", "b", "--follow"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}
