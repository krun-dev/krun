package control

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/krun-dev/krun/internal/adapters/bucketobjects"
	"github.com/krun-dev/krun/internal/adapters/storagecredentials"
	"github.com/krun-dev/krun/internal/domain"
)

func TestObjectBrowserAuthorizationAndControlOnly(t *testing.T) {
	s, store, _ := cfTestServer(t)
	enableTestObjectStorage(t, s)
	var requests atomic.Int32
	var listOutsideLock atomic.Bool
	prefix := "worker-data/v1/org/org-a/buckets/bkt-fixture/"
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Body != nil {
			var data [1]byte
			if n, _ := r.Body.Read(data[:]); n != 0 {
				t.Error("object body reached control-plane SDK")
			}
		}
		switch r.Method {
		case http.MethodGet:
			if r.URL.Query().Get("prefix") != prefix {
				t.Error("incorrect physical bucket scope")
			}
			// A storage round trip must not block an unrelated state read.
			done := make(chan struct{})
			go func() {
				_ = store.View(context.Background(), PartitionAll, func(*State) error { return nil })
				close(done)
			}()
			select {
			case <-done:
				listOutsideLock.Store(true)
			case <-time.After(time.Second):
				t.Error("storage operation held a state lock")
			}
			w.Header().Set("Content-Type", "application/xml")
			_, _ = fmt.Fprintf(w, `<ListBucketResult><Name>worker-data</Name><Prefix>%s</Prefix><EncodingType>url</EncodingType><IsTruncated>false</IsTruncated><Contents><Key>%shello.txt</Key><Size>5</Size><ETag>"etag-fixture"</ETag><LastModified>2026-11-01T00:00:00Z</LastModified></Contents></ListBucketResult>`, prefix, prefix)
		case http.MethodHead:
			if r.URL.Path != "/worker-data/"+prefix+"hello.txt" {
				t.Error("HEAD escaped logical scope")
			}
			w.Header().Set("Content-Length", "5")
			w.Header().Set("Last-Modified", "Sun, 01 Nov 2026 00:00:00 GMT")
			w.Header().Set("ETag", `"etag-fixture"`)
			w.Header().Set("X-Amz-Meta-Krun-V1", base64.StdEncoding.EncodeToString([]byte(`{"version":"fixture-version","customMetadata":{"owner":"fixture"},"httpMetadata":{"contentType":"text/plain"}}`)))
		case http.MethodDelete:
			if r.URL.Path != "/worker-data/"+prefix+"hello.txt" {
				t.Error("DELETE escaped logical scope")
			}
			w.WriteHeader(204)
		default:
			t.Error("unexpected data-plane request through the control plane")
			w.WriteHeader(500)
		}
	}))
	defer backend.Close()
	s.Config.Workers.ObjectStorage.Stores[0].Endpoint = backend.URL
	registry, err := storagecredentials.NewRegistry(s.Config.Workers.ObjectStorage)
	if err != nil {
		t.Fatal(err)
	}
	s.bucketObjects = bucketobjects.New(s.Config.Workers.ObjectStorage, registry)
	if err := store.Update(context.Background(), PartitionWorkers, func(st *State) error {
		st.Workers.Buckets["bkt-fixture"] = domain.WorkerBucket{ID: "bkt-fixture", Organization: "org-a", Name: "images", Store: "primary", Enabled: true, Revision: 1}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	base := "/api/v1/orgs/org-a/buckets/bkt-fixture/objects"
	for _, method := range []string{"DELETE", "POST"} {
		path, data := base, ""
		if method == "POST" {
			path += "/access"
			data = `{"key":"hello.txt","operation":"upload","size":5}`
		}
		if out := request(s, method, path, "krun_cli_viewer", data); out.Code != 403 {
			t.Fatalf("viewer gained object write access: %d", out.Code)
		}
	}
	if requests.Load() != 0 {
		t.Fatal("denied request contacted storage")
	}
	viewerList := request(s, "GET", base, "krun_cli_viewer", "")
	if viewerList.Code != 200 || !strings.Contains(viewerList.Body.String(), `"key":"hello.txt"`) {
		t.Fatal("viewer cannot list objects", viewerList.Code, viewerList.Body.String())
	}
	list := request(s, "GET", base, "krun_cli_admin", "")
	if list.Code != 200 || !strings.Contains(list.Body.String(), `"key":"hello.txt"`) || strings.Contains(list.Body.String(), prefix) || !listOutsideLock.Load() {
		t.Fatal("logical listing failed", list.Code, list.Body.String())
	}
	head := request(s, "GET", base+"/head?key=hello.txt", "krun_cli_admin", "")
	if head.Code != 200 || !strings.Contains(head.Body.String(), `"r2_compatible":true`) || !strings.Contains(head.Body.String(), `"owner":"fixture"`) {
		t.Fatal("object detail failed", head.Code, head.Body.String())
	}
	cf := request(s, "GET", cfPrefix+"/accounts/org-a/r2/buckets/images/objects?per_page=1", "krun_cli_admin", "")
	if cf.Code != 200 || !strings.Contains(cf.Body.String(), `"result_info":`) || !strings.Contains(cf.Body.String(), `"last_modified":`) || strings.Contains(cf.Body.String(), prefix) {
		t.Fatal("Cloudflare object-list envelope failed", cf.Code, cf.Body.String())
	}
	before := requests.Load()
	grant := request(s, "POST", base+"/access", "krun_cli_admin", `{"key":"hello.txt","operation":"upload","size":5,"http_metadata":{"contentType":"text/plain"}}`)
	var access domain.BucketObjectAccess
	if grant.Code != 200 || json.Unmarshal(grant.Body.Bytes(), &access) != nil {
		t.Fatal("signing failed", grant.Code, grant.Body.String())
	}
	u, _ := url.Parse(access.URL)
	signed := u.Query().Get("X-Amz-SignedHeaders")
	if !strings.Contains(signed, "x-amz-meta-krun-v1") || !strings.Contains(signed, "content-length") || access.Headers["X-Amz-Meta-Krun-V1"] == "" || strings.Contains(grant.Body.String(), "example-secret") {
		t.Fatal("upload capability omitted signed metadata/size or disclosed a secret")
	}
	redirect := request(s, "GET", base+"/download?key=hello.txt", "krun_cli_admin", "")
	if redirect.Code != 302 || redirect.Header().Get("Location") == "" || !strings.Contains(redirect.Header().Get("Cache-Control"), "no-store") || requests.Load() != before {
		t.Fatal("download or signing forwarded object traffic")
	}
	for _, path := range []string{base + "/head?key=../escape", strings.Replace(base, "org-a", "org-b", 1), base + "?limit=1001"} {
		out := request(s, "GET", path, "krun_cli_admin", "")
		if out.Code < 400 {
			t.Fatal("invalid scope accepted", path)
		}
	}
	if requests.Load() != before {
		t.Fatal("invalid request contacted storage")
	}
	if out := request(s, "DELETE", base+"?key=hello.txt", "krun_cli_admin", ""); out.Code != 200 {
		t.Fatal("delete failed", out.Code, out.Body.String())
	}
	if err := store.Update(context.Background(), PartitionWorkers, func(st *State) error {
		bucket := st.Workers.Buckets["bkt-fixture"]
		bucket.Enabled = false
		st.Workers.Buckets[bucket.ID] = bucket
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if out := request(s, "POST", base+"/access", "krun_cli_admin", `{"key":"hello.txt","operation":"read"}`); out.Code != 404 {
		t.Fatal("disabled bucket issued a new capability", out.Code)
	}
}
