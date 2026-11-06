package control

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/krun-dev/krun/internal/adapters/objectstore"
	workerapp "github.com/krun-dev/krun/internal/workers"
)

func TestWorkerBuildLogDelivery(t *testing.T) {
	s, store, base := workerTestServer(t)
	var reads atomic.Int32
	objects := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		if r.Method != http.MethodGet || !strings.HasPrefix(r.URL.Path, "/bucket/build-logs/") {
			t.Errorf("unexpected object request: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Cookie") != "" || strings.Contains(r.Header.Get("Authorization"), "krun_cli_") {
			t.Error("control-plane credentials forwarded to object storage")
		}
		if strings.HasSuffix(r.URL.Path, "/missing") {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`<Error><Code>NoSuchKey</Code></Error>`))
			return
		}
		text := "build complete\n"
		if strings.HasSuffix(r.URL.Path, "/oversized") {
			text = strings.Repeat("x", 32769)
		}
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Content-Length", strconv.Itoa(len(text)))
		w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
		_, _ = w.Write([]byte(text))
	}))
	defer objects.Close()
	s.Config.Workers.S3Endpoint = objects.URL
	s.Config.Workers.S3Bucket = "bucket"
	s.Config.Workers.S3Region = "us-east-1"
	s.Config.Workers.S3AccessKey = "test-access"
	s.Config.Workers.S3SecretKey = "test-secret"
	client, err := objectstore.NewWorkerS3(s.Config.Workers)
	if err != nil {
		t.Fatal(err)
	}
	s.workers = &workerapp.Controller{Config: s.Config.Workers, S3: client}
	b := seedWorkerBuild(t, s, store, base)
	path := base + "/builds/" + b.ID + "/logs"

	for _, tc := range []struct {
		name, query, state, key string
		expired                 bool
		status                  int
		objectReads             int32
	}{
		{name: "default direct", state: "succeeded", key: "snapshot", status: 200},
		{name: "explicit direct", query: "?inline=false", state: "succeeded", key: "snapshot", status: 200},
		{name: "inline active", query: "?inline=true", state: "building", key: "snapshot", status: 200, objectReads: 1},
		{name: "inline terminal", query: "?inline=true", state: "failed", key: "snapshot", status: 200, objectReads: 1},
		{name: "inline no snapshot", query: "?inline=true", state: "building", status: 200},
		{name: "inline expired", query: "?inline=true", state: "succeeded", key: "snapshot", expired: true, status: 200},
		{name: "direct expired", state: "succeeded", key: "snapshot", expired: true, status: 200},
		{name: "inline missing object", query: "?inline=true", state: "succeeded", key: "missing", status: 503, objectReads: 1},
		{name: "inline oversized object", query: "?inline=true", state: "succeeded", key: "oversized", status: 503, objectReads: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := store.Update(context.Background(), PartitionAll, func(st *State) error {
				build := st.Workers.Builds[b.ID]
				build.State = tc.state
				build.Created = time.Now()
				if tc.expired {
					build.Created = build.Created.Add(-8 * 24 * time.Hour)
				}
				st.Workers.Builds[b.ID] = build
				private := st.Workers.BuildPrivate[b.ID]
				private.LogKey = ""
				if tc.key != "" {
					private.LogKey = "build-logs/" + tc.key
				}
				st.Workers.BuildPrivate[b.ID] = private
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			before := reads.Load()
			response := request(s, http.MethodGet, path+tc.query, "krun_cli_admin", "")
			if response.Code != tc.status || reads.Load()-before != tc.objectReads {
				t.Fatalf("status=%d reads=%d body=%s", response.Code, reads.Load()-before, response.Body.String())
			}
			if tc.status != http.StatusOK {
				if !strings.Contains(response.Body.String(), "LOG_UNAVAILABLE") {
					t.Fatal(response.Body.String())
				}
				return
			}
			var out workerapp.WorkerBuildLogsResult
			if err := json.Unmarshal(response.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
			if out.BuildID != b.ID || out.State != tc.state || out.Expired != tc.expired {
				t.Fatalf("unexpected metadata: %+v", out)
			}
			if tc.query == "?inline=true" {
				want := ""
				if tc.objectReads > 0 {
					want = "build complete\n"
				}
				if out.Log == nil || *out.Log != want || out.DownloadURL != "" {
					t.Fatalf("unexpected inline log: %+v", out)
				}
			} else if out.Log != nil || (out.DownloadURL != "") != (tc.key != "" && !tc.expired) {
				t.Fatalf("unexpected direct log: %+v", out)
			}
			poll := ""
			if tc.state == "building" {
				poll = "10"
			}
			if response.Header().Get("X-Log-Poll-Interval") != poll || response.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatal("log polling/cache headers changed", response.Header())
			}
		})
	}

	// A key that can inspect build metadata still needs log.read in either mode.
	issued := request(s, http.MethodPost, "/api/v1/orgs/org-a/api-keys", "krun_cli_viewer",
		`{
			"name": "build metadata",
			"expires_in": 3600,
			"policy": {
				"schema_version": 1,
				"statements": [{
					"effect": "allow",
					"actions": ["worker.version.read"],
					"resource": {
						"organization_id": "org-a",
						"kind": "worker",
						"ids": ["svc-a"]
					},
					"conditions": {}
				}]
			}
		}`)
	var key struct{ Key string }
	if err := json.Unmarshal(issued.Body.Bytes(), &key); err != nil || issued.Code != http.StatusCreated {
		t.Fatalf("issue metadata key: %d %s", issued.Code, issued.Body.String())
	}
	for _, query := range []string{"", "?inline=true"} {
		before := reads.Load()
		for _, tc := range []struct {
			path, token string
			status      int
		}{
			{path, "", 401},
			{path, key.Key, 403},
			{strings.Replace(path, "org-a", "org-b", 1), "krun_cli_admin", 404},
		} {
			response := request(s, http.MethodGet, tc.path+query, tc.token, "")
			if response.Code != tc.status || reads.Load() != before {
				t.Fatalf("unauthorized log access: %d %s", response.Code, response.Body.String())
			}
		}
	}
}
