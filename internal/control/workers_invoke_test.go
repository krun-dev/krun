package control

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"time"
)

func publishedWorkerInvokeServer(t *testing.T) (*Server, *Store, string, string) {
	t.Helper()
	s, store, base := workerTestServer(t)
	s.Config.Workers.Gateways = []WorkerGateway{{ID: "internal", Name: "workers-gateway", Namespace: "gateway-system", Section: "private"}}
	s.Config.Workers.Domains = []WorkerDomain{{ID: "internal", Domain: "workers.test", Label: "内网", Gateway: "internal", Default: true}}
	var host string
	if err := store.Update(context.Background(), func(st *State) error {
		service := s.workerService(st, Target{ID: "svc-a", Organization: "org-a"})
		env := service.Environments["production"]
		env.Release = "rel-test"
		service.Environments["production"] = env
		st.Workers.Services[service.Service] = service
		host = env.Hosts[0]
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return s, store, base + "/environments/production/invoke", host
}

func TestWorkerInvokeGatewayRoutingAndResponseIsolation(t *testing.T) {
	s, store, endpoint, host := publishedWorkerInvokeServer(t)
	var calls atomic.Int32
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path == "/redirect-target" {
			t.Error("business redirect was followed")
			return
		}
		if r.Host != host || r.Method != http.MethodPost {
			t.Errorf("wrong target: host=%q method=%q", r.Host, r.Method)
		}
		if !strings.HasSuffix(r.RequestURI, "?q=a%2Fb") {
			t.Errorf("query changed: %q", r.RequestURI)
		}
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("X-CSRF-Token") != "" {
			t.Error("platform credential forwarded to business Worker")
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil || string(raw) != `{"message":"hello"}` || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected request body: %q, %v", raw, err)
		}
		// The IAM state must remain available while the Worker is running.
		updated := make(chan error, 1)
		go func() { updated <- store.Update(context.Background(), func(*State) error { return nil }) }()
		select {
		case err := <-updated:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(time.Second):
			t.Error("Worker invocation held the state transaction lock")
		}
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("X-Krun-Release", "rel-test")
		w.Header().Set("X-Krun-Pod", "pod-test")
		w.Header().Set("Set-Cookie", "business-session=secret")
		w.Header().Set("Location", "/redirect-target")
		w.Header().Set("X-Business-Secret", "secret")
		if r.URL.Path == "/redirect" {
			w.WriteHeader(http.StatusFound)
		} else {
			w.WriteHeader(http.StatusBadGateway)
		}
		_, _ = io.WriteString(w, `<script>alert("business")</script>`)
	}))
	defer gateway.Close()
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			if address != "workers-gateway.gateway-system.svc:8080" {
				t.Errorf("request escaped configured gateway: %q", address)
			}
			return (&net.Dialer{}).DialContext(ctx, network, gateway.Listener.Addr().String())
		},
	}
	defer transport.CloseIdleConnections()
	s.workerInvokeClient.Transport = transport
	for _, tc := range []struct {
		path   string
		status int
	}{{"/business%2Fnested", http.StatusBadGateway}, {"/redirect", http.StatusFound}} {
		t.Run(tc.path, func(t *testing.T) {
			payload, _ := json.Marshal(workerInvokeRequest{Path: tc.path + "?q=a%2Fb", Method: http.MethodPost, Body: `{"message":"hello"}`})
			r := request(s, http.MethodPost, endpoint, "krun_cli_admin", string(payload))
			if r.Code != http.StatusOK || !strings.HasPrefix(r.Header().Get("Content-Type"), "application/json") {
				t.Fatalf("business response served directly: %d, %s", r.Code, r.Body.String())
			}
			if r.Header().Get("Set-Cookie") != "" || r.Header().Get("Location") != "" {
				t.Fatal("business cookie or redirect reached console origin")
			}
			var out workerInvokeResponse
			if err := json.Unmarshal(r.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
			if out.Status != tc.status || out.Body != `<script>alert("business")</script>` || out.Truncated {
				t.Fatalf("wrong business response: %+v", out)
			}
			if len(out.Headers) != 3 || out.Headers["X-Krun-Release"] != "rel-test" || out.Headers["X-Krun-Pod"] != "pod-test" || out.Headers["Content-Type"] != "text/html" {
				t.Fatalf("wrong response headers: %+v", out.Headers)
			}
		})
	}
	if calls.Load() != 2 {
		t.Fatalf("unexpected gateway calls: %d", calls.Load())
	}
}

type invokeRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn invokeRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }

func TestWorkerInvokeResponseLimit(t *testing.T) {
	for _, tc := range []struct {
		name string
		size int
	}{{"at_limit", 64 << 10}, {"over_limit", (64 << 10) + 1}} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, endpoint, host := publishedWorkerInvokeServer(t)
			s.workerInvokeClient.Transport = invokeRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.Method != http.MethodGet || r.URL.Path != "/" || r.Host != host {
					t.Errorf("wrong defaults or host: %s %s %s", r.Method, r.URL.Path, r.Host)
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(strings.Repeat("x", tc.size)))}, nil
			})
			r := request(s, http.MethodPost, endpoint, "krun_cli_admin", `{}`)
			var out workerInvokeResponse
			if err := json.Unmarshal(r.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
			if r.Code != 200 || len(out.Body) != 64<<10 || out.Truncated != (tc.size > 64<<10) {
				t.Fatalf("size=%d status=%d response size=%d truncated=%v", tc.size, r.Code, len(out.Body), out.Truncated)
			}
		})
	}
}

func TestWorkerInvokeUpstreamFailures(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response *http.Response
		err      error
	}{
		{"connection_failure", nil, errors.New("internal gateway details")},
		{"interrupted_body", &http.Response{StatusCode: 200, Body: io.NopCloser(iotest.ErrReader(errors.New("internal body details")))}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, endpoint, _ := publishedWorkerInvokeServer(t)
			s.workerInvokeClient.Transport = invokeRoundTripFunc(func(*http.Request) (*http.Response, error) {
				return tc.response, tc.err
			})
			r := request(s, http.MethodPost, endpoint, "krun_cli_admin", `{}`)
			if r.Code != 503 || !strings.Contains(r.Body.String(), "WORKER_INVOKE_UNAVAILABLE") || strings.Contains(r.Body.String(), "internal") {
				t.Fatal(r.Code, r.Body.String())
			}
		})
	}
}

func TestWorkerInvokeRejectsInvalidRequestsWithoutNetwork(t *testing.T) {
	s, _, endpoint, _ := publishedWorkerInvokeServer(t)
	s.workerInvokeClient.Transport = invokeRoundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Error("invalid request reached the network")
		return nil, errors.New("unexpected network call")
	})
	cases := []workerInvokeRequest{
		{Path: "http://attacker.test/"}, {Path: "//attacker.test/"}, {Path: "relative"},
		{Path: "/%invalid"}, {Path: "/\r\nHost: attacker.test"},
		{Path: "/" + strings.Repeat("x", 2048)}, {Body: strings.Repeat("x", (64<<10)+1)},
		{Method: "CONNECT"}, {Method: "TRACE"},
	}
	for i, in := range cases {
		payload, _ := json.Marshal(in)
		r := request(s, http.MethodPost, endpoint, "krun_cli_admin", string(payload))
		if r.Code != 422 {
			t.Fatalf("case %d: status=%d body=%s", i, r.Code, r.Body.String())
		}
	}
	if r := request(s, http.MethodPost, endpoint, "", `{}`); r.Code != 401 {
		t.Fatal(r.Code, r.Body.String())
	}
	if r := request(s, http.MethodPost, endpoint, "krun_cli_viewer", `{}`); r.Code != 403 {
		t.Fatal(r.Code, r.Body.String())
	}
}

func TestWorkerInvokeSessionCSRF(t *testing.T) {
	s, store, endpoint, _ := publishedWorkerInvokeServer(t)
	if err := store.Update(context.Background(), func(st *State) error {
		session := st.Sessions[digest("krun_cli_admin")]
		session.CLI = false
		session.CSRF = "expected-csrf"
		st.Sessions[digest("browser-admin")] = session
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var calls int
	s.workerInvokeClient.Transport = invokeRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Header.Get("Cookie") != "" || r.Header.Get("X-CSRF-Token") != "" {
			t.Error("browser credentials forwarded")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok"))}, nil
	})
	for _, tc := range []struct {
		origin string
		csrf   string
		status int
	}{
		{s.Config.PublicURL, "", 403},
		{s.Config.PublicURL, "wrong-csrf", 403},
		{"https://attacker.test", "expected-csrf", 403},
		{s.Config.PublicURL, "expected-csrf", 200},
	} {
		r := httptest.NewRequest(http.MethodPost, endpoint, strings.NewReader(`{}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", tc.origin)
		r.Header.Set("X-CSRF-Token", tc.csrf)
		r.AddCookie(&http.Cookie{Name: s.cookieName("session"), Value: "browser-admin"})
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("origin=%q csrf=%q: status=%d body=%s", tc.origin, tc.csrf, w.Code, w.Body.String())
		}
	}
	if calls != 1 {
		t.Fatalf("CSRF validation did not gate network access: calls=%d", calls)
	}
}

func TestWorkerInvokeCanceledRequest(t *testing.T) {
	s, _, endpoint, _ := publishedWorkerInvokeServer(t)
	s.workerInvokeClient.Transport = invokeRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	r := httptest.NewRequest(http.MethodPost, endpoint, strings.NewReader(`{}`)).WithContext(ctx)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer krun_cli_admin")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 503 || !strings.Contains(w.Body.String(), "WORKER_INVOKE_UNAVAILABLE") {
		t.Fatal(w.Code, w.Body.String())
	}
}
