package workers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/krun-dev/krun/internal/adapters/kubernetes"
	appconfig "github.com/krun-dev/krun/internal/config"
)

type sessionRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn sessionRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return fn(r)
}

func TestVerifyRuntimeSessionUsesPodManagementIdentity(t *testing.T) {
	proof := strings.Repeat("a", 64)
	pool := appconfig.WorkerRuntimePool{ID: "default", ManagementToken: "fixture-management"}
	for _, test := range []struct {
		name     string
		uid      string
		proof    string
		identity map[string]any
		status   int
		wantOK   bool
	}{
		{"bootstrapping Pod", "pod", proof, map[string]any{"pod_uid": "pod", "pool": "default"}, 200, true},
		{"forged proof", "pod", strings.Repeat("b", 64), map[string]any{"pod_uid": "pod", "pool": "default"}, 200, false},
		{"missing proof", "pod", "", nil, 200, false},
		{"unknown Pod", "unknown", proof, nil, 200, false},
		{"wrong Pod", "pod", proof, map[string]any{"pod_uid": "other", "pool": "default"}, 200, false},
		{"wrong pool", "pod", proof, map[string]any{"pod_uid": "pod", "pool": "other"}, 200, false},
		{"old runtime", "pod", proof, map[string]any{"error": "not found"}, 404, false},
		{"management failure", "pod", proof, map[string]any{"pod_uid": "pod", "pool": "default"}, 503, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/namespaces/test/pods" || r.URL.Query().Get("labelSelector") != "role=krun-runtime,pool=default" {
					t.Errorf("unexpected Kubernetes lookup: %s", r.URL)
				}
				// No Ready condition: authentication must work before readiness.
				io.WriteString(w, `{"items":[{"metadata":{"name":"runtime-0","uid":"pod"},"status":{"podIP":"127.0.0.2"}}]}`)
			}))
			defer api.Close()
			controller := &Controller{
				Kube: &kubernetes.Client{Base: api.URL, Namespace: "test", Client: api.Client()},
				Http: &http.Client{Transport: sessionRoundTripFunc(func(r *http.Request) (*http.Response, error) {
					if r.URL.String() != "http://127.0.0.2:8083/config-session" || r.Header.Get("Authorization") != "Bearer "+pool.ManagementToken {
						t.Errorf("unexpected identity probe: %s", r.URL)
					}
					status := test.status
					if r.Header.Get("X-Krun-Session-Proof") != proof {
						status = http.StatusForbidden
					}
					body, _ := json.Marshal(test.identity)
					return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
				})},
			}
			if err := controller.VerifyRuntimeSession(context.Background(), pool, test.uid, test.proof); (err == nil) != test.wantOK {
				t.Fatalf("verification error=%v, wantOK=%v", err, test.wantOK)
			}
		})
	}
}
