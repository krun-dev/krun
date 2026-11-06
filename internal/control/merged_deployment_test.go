package control

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestMergedDeploymentPreservesRuntimeAuthentication(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		name := "split"
		if enabled {
			name = "merged"
		}
		t.Run(name, func(t *testing.T) {
			s, _, _ := workerTestServer(t)
			internal := httptest.NewServer(http.HandlerFunc(s.serveInternal))
			defer internal.Close()
			s.Config.InternalListen = strings.TrimPrefix(internal.URL, "http://")
			s.Config.MergedDeployment = enabled
			s.routes()
			public := httptest.NewServer(s)
			defer public.Close()
			pool := s.Config.Workers.DefaultRuntimePool()
			for _, test := range []struct {
				path   string
				token  string
				pool   string
				status int
			}{
				{"/api/internal/workers/config/snapshot", pool.RuntimeToken, pool.ID, http.StatusOK},
				{"/api/internal/workers/config/snapshot", "", pool.ID, http.StatusForbidden},
				{"/api/internal/workers/config/snapshot", "wrong-token", pool.ID, http.StatusForbidden},
				{"/api/internal/workers/config/snapshot", pool.RuntimeToken, "another-pool", http.StatusForbidden},
				{"/api/internal/workers/unknown", pool.RuntimeToken, pool.ID, http.StatusNotFound},
				{"/api/internal/workers-evil/config/snapshot", pool.RuntimeToken, pool.ID, http.StatusNotFound},
			} {
				req, err := http.NewRequest(http.MethodGet, public.URL+test.path, nil)
				if err != nil {
					t.Fatal(err)
				}
				if test.token != "" {
					req.Header.Set("Authorization", "Bearer "+test.token)
				}
				req.Header.Set("X-Runtime-Pool", test.pool)
				response, err := public.Client().Do(req)
				if err != nil {
					t.Fatal(err)
				}
				raw, err := io.ReadAll(response.Body)
				response.Body.Close()
				if err != nil {
					t.Fatal(err)
				}
				expected := test.status
				if !enabled {
					expected = http.StatusNotFound
				}
				if response.StatusCode != expected {
					t.Fatalf("%s: status=%d expected=%d body=%s", test.path, response.StatusCode, expected, raw)
				}
				if response.Header.Get("Cache-Control") != "no-store" {
					t.Fatal("Runtime API response is cacheable")
				}
				if enabled && expected == http.StatusOK {
					var payload struct {
						Pool string `json:"pool"`
					}
					if err := json.Unmarshal(raw, &payload); err != nil || payload.Pool != pool.ID {
						t.Fatal("wrong runtime pool response")
					}
				}
			}
			response, err := public.Client().Get(public.URL + "/api/healthz")
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Fatal("business health endpoint changed")
			}
		})
	}
}

func TestMergedDeploymentForwardsMethodBodyAndQuery(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodPost || r.URL.RequestURI() != "/api/internal/workers/ack?fixture=1" || string(body) != `{"pool":"default"}` {
			t.Error("proxy changed method, request target or body")
		}
		if r.Header.Get("Authorization") != "Bearer fixture-token" || r.Header.Get("X-Runtime-Pool") != "default" {
			t.Error("proxy dropped runtime authentication")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()
	s, _, _ := workerTestServer(t)
	// Exercise the wildcard bind mapping: the internal listener is reached over
	// loopback, rather than dialing an unspecified address or another replica.
	address := strings.TrimPrefix(upstream.URL, "http://127.0.0.1:")
	s.Config.InternalListen = "0.0.0.0:" + address
	s.Config.MergedDeployment = true
	s.routes()
	public := httptest.NewServer(s)
	defer public.Close()
	req, err := http.NewRequest(http.MethodPost, public.URL+"/api/internal/workers/ack?fixture=1", strings.NewReader(`{"pool":"default"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer fixture-token")
	req.Header.Set("X-Runtime-Pool", "default")
	req.Header.Set("Content-Type", "application/json")
	response, err := public.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent || calls.Load() != 1 {
		t.Fatal("request was not forwarded exactly once")
	}
}

func TestMergedDeploymentUnavailableInternalListener(t *testing.T) {
	upstream := httptest.NewServer(http.NotFoundHandler())
	address := strings.TrimPrefix(upstream.URL, "http://")
	upstream.Close()
	s, _, _ := workerTestServer(t)
	s.Config.InternalListen = address
	s.Config.MergedDeployment = true
	s.routes()
	public := httptest.NewServer(s)
	defer public.Close()
	response, err := public.Client().Get(public.URL + "/api/internal/workers/config/snapshot")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("unexpected proxy failure status %d", response.StatusCode)
	}
}
