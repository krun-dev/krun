package cli

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDeploymentPropagationCommand(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/api/v1/orgs/team/workers/worker-a/environments/preview/propagation" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"state":"current","target_generation":42,"membership_confirmed":true,"applied":1,"unsubscribed":2,"total":3}`)
	}))
	defer server.Close()
	for _, wait := range []bool{false, true} {
		out := new(bytes.Buffer)
		client := Client{Profile: Profile{Endpoint: server.URL}, Out: out, Err: io.Discard}
		args := []string{"propagation", "--org", "team", "--worker", "worker-a", "--env", "preview"}
		if wait {
			args = append(args, "--wait", "--timeout", "1s")
		}
		if err := client.deployments(context.Background(), args); err != nil {
			t.Fatal(err)
		}
		if out.Len() == 0 {
			t.Fatal("missing propagation result")
		}
	}
}
func TestPropagationWaitDoesNotTreatUnknownAsConverged(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"state":"unknown","unknown":1}`)
	}))
	defer server.Close()
	client := Client{Profile: Profile{Endpoint: server.URL}, Out: io.Discard, Err: io.Discard}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := client.deployments(ctx, []string{"propagation", "--org", "team", "--worker", "worker-a", "--wait", "--timeout", "1s"}); err == nil {
		t.Fatal("unknown members reported convergence")
	}
}
