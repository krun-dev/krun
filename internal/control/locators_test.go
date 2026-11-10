package control

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestNativeAndCloudflareResourceLocators(t *testing.T) {
	s, store, _ := workerTestServer(t)
	exerciseResourceLocators(t, s)
	expireTestSession(t, store, "krun_cli_admin")
	for _, route := range []string{
		"/api/v1/orgs/platform/workers/tu-pian-chu-li",
		"/api/v1/orgs/platform/workers/unknown",
		"/api/v1/orgs/unknown/workers/unknown",
	} {
		if result := request(s, http.MethodGet, route, "krun_cli_admin", ""); result.Code != 401 {
			t.Fatalf("expired session on %s: %d %s", route, result.Code, result.Body)
		}
	}
}

func TestPostgresNativeAndCloudflareResourceLocators(t *testing.T) {
	s, _, _ := workerTestServer(t)
	const token = "krun_cli_admin"
	exerciseResourceLocators(t, s)
	const route = "/api/v1/orgs/platform/workers"
	start := make(chan struct{})
	responses := make(chan int, 2)
	for _, name := range []string{"Concurrent Worker", "CONCURRENT-WORKER"} {
		go func() {
			<-start
			response := request(s, http.MethodPost, route, token, `{"name":"`+name+`"}`)
			responses <- response.Code
		}()
	}
	close(start)
	first, second := <-responses, <-responses
	if min(first, second) != 201 || max(first, second) != 409 {
		t.Fatalf("concurrent create returned %d and %d, want one creation and one conflict", first, second)
	}

}

func exerciseResourceLocators(t *testing.T, s *Server) {
	t.Helper()
	call := func(method, route, body string, want int) []byte {
		t.Helper()
		res := request(s, method, route, "krun_cli_admin", body)
		if res.Code != want {
			t.Fatalf("%s %s: got %d, want %d: %s", method, route, res.Code, want, res.Body)
		}
		return res.Body.Bytes()
	}
	var org Organization
	raw := call(http.MethodPost, "/api/v1/orgs", `{"name":"平台团队","short_name":"  ＰＬＡＴＦＯＲＭ  "}`, 201)
	if err := json.Unmarshal(raw, &org); err != nil || org.ShortName != "platform" || org.Name != "平台团队" {
		t.Fatalf("organization response: %s (%v)", raw, err)
	}
	call(http.MethodPost, "/api/v1/orgs", `{"name":"Other","short_name":"platform"}`, 409)
	const base = "/api/v1/orgs/platform"
	call(http.MethodGet, base+"/members", "", 200)
	call(http.MethodPatch, base, `{"name":"改名后的团队","allowed_sources":[],"expected_revision":1}`, 200)

	var worker Resource
	raw = call(http.MethodPost, base+"/workers", `{"name":"图片处理"}`, 201)
	if err := json.Unmarshal(raw, &worker); err != nil || worker.Locator != "tu-pian-chu-li" || worker.Name != "图片处理" || worker.ResourcePath != "platform/workers/tu-pian-chu-li" {
		t.Fatalf("Worker response: %s (%v)", raw, err)
	}
	const workerRoute = base + "/workers/tu-pian-chu-li"
	call(http.MethodPost, base+"/workers", `{"name":"TU PIAN CHU LI"}`, 409)
	call(http.MethodPost, base+"/models", `{"name":"图片处理"}`, 201)
	call(http.MethodPost, "/api/v1/orgs/org-b/workers", `{"name":"图片处理"}`, 201)
	call(http.MethodPatch, workerRoute, `{"name":"改名后的 Worker","expected_revision":1}`, 200)
	call(http.MethodGet, workerRoute+"/overview", "", 200)
	call(http.MethodGet, base+"/workers?worker=tu-pian-chu-li", "", 200)
	call(http.MethodPost, "/api/v1/authz/check", `{"action":"worker.read","resource_kind":"worker","resource_id":"platform/workers/tu-pian-chu-li"}`, 200)
	key := call(http.MethodPost, base+"/api-keys", `{
		"name":"locator test","expires_in":3600,
		"policy":{"schema_version":1,"statements":[{
			"effect":"allow","actions":["worker.read"],
			"resource":{"kind":"worker","organization_id":"platform","ids":["platform/workers/tu-pian-chu-li"]}
		}]}
	}`, 201)
	if !strings.Contains(string(key), worker.ID) || strings.Contains(string(key), worker.ResourcePath) {
		t.Fatalf("policy must store the internal identity: %s", key)
	}

	for _, account := range []string{org.ID, org.ShortName} {
		path := cfPrefix + "/accounts/" + account + "/workers/workers/tu-pian-chu-li"
		var response struct {
			Result struct {
				Name string `json:"name"`
				Tag  string `json:"tag"`
			} `json:"result"`
		}
		raw = call(http.MethodGet, path, "", 200)
		if err := json.Unmarshal(raw, &response); err != nil || response.Result.Tag != worker.ID || response.Result.Name != worker.Locator {
			t.Fatalf("Cloudflare response: %s (%v)", raw, err)
		}
	}
}
