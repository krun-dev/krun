package control

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"
)

func cfTestServer(t *testing.T) (*Server, *Store, string) {
	s, store, _ := workerTestServer(t)
	if _, err := postgresTestPool(store).Exec(t.Context(), "UPDATE krun_resources SET locator='fixture' WHERE resource_id='svc-a'"); err != nil {
		t.Fatal(err)
	}
	return s, store, cfPrefix + "/accounts/org-a/workers/scripts/fixture"
}
func TestCloudflareEnvelopeAuthenticationAndIsolation(t *testing.T) {
	s, _, base := cfTestServer(t)
	for _, tc := range []struct {
		url, token string
		status     int
	}{{base + "/settings", "", 401}, {base + "/settings", "krun_cli_admin", 200}, {strings.Replace(base, "org-a", "org-b", 1) + "/settings", "krun_cli_admin", 404}, {base + "/unknown", "krun_cli_admin", 404}, {cfPrefix + "/accounts", "krun_cli_admin", 200}} {
		r := request(s, "GET", tc.url, tc.token, "")
		if r.Code != tc.status {
			t.Fatalf("%s: %d %s", tc.url, r.Code, r.Body.String())
		}
		var out struct {
			Success bool
			Errors  []struct {
				Code    int
				Message string
			}
			Result json.RawMessage
		}
		if err := json.Unmarshal(r.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if out.Success != (tc.status == 200) || tc.status != 200 && (len(out.Errors) != 1 || out.Errors[0].Code == 0 || out.Errors[0].Message == "") {
			t.Fatalf("invalid envelope: %s", r.Body.String())
		}
	}
}
func TestCloudflarePreviewsBaseCopiesAndMasksSecrets(t *testing.T) {
	s, store, _ := cfTestServer(t)
	enableWorkerSecrets(s.Config.Workers)
	base := cfPrefix + "/accounts/org-a/workers/workers/fixture"
	r := request(s, "PATCH", base, "krun_cli_admin", `{"previews_base_config":{"env":{"apiToken":{"type":"secret_text","text":"masked-fixture"},"label":{"type":"plain_text","text":"base"}}}}`)
	if r.Code != 200 || strings.Contains(r.Body.String(), "masked-fixture") {
		t.Fatalf("base: %d %s", r.Code, r.Body.String())
	}
	r = request(s, "POST", base+"/previews", "krun_cli_admin", `{"name":"branch-one"}`)
	if r.Code != 200 {
		t.Fatalf("preview: %d %s", r.Code, r.Body.String())
	}
	if err := store.View(context.Background(), PartitionWorkers, func(st *State) error {
		service := st.Workers.Workers["svc-a"]
		if service.Environments["branch-one"].SecretVersions["apiToken"] == "" || service.Environments["branch-one"].Variables["label"] != "base" || len(service.Environments["production"].SecretVersions) != 0 {
			t.Fatal("Preview base isolation failed")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
func TestCloudflareRejectsUnsupportedTrafficAndBindings(t *testing.T) {
	s, _, base := cfTestServer(t)
	r := request(s, "POST", base+"/deployments", "krun_cli_admin", `{"strategy":"percentage","versions":[{"version_id":"a","percentage":50},{"version_id":"b","percentage":50}]}`)
	if r.Code != 422 {
		t.Fatalf("traffic split accepted: %d %s", r.Code, r.Body.String())
	}
	if _, err := cfBindingInput(cfUploadMetadata{Bindings: []cfBinding{{Name: "DB", Type: "d1"}}}, false); err == nil {
		t.Fatal("D1 binding accepted")
	}
	r = request(s, "POST", base+"/subdomain", "krun_cli_admin", `{"enabled":false}`)
	if r.Code != 422 {
		t.Fatal("disabled URL reported as supported")
	}
}
func TestCloudflareMultipartRetainsNestedModuleNames(t *testing.T) {
	var raw bytes.Buffer
	mw := multipart.NewWriter(&raw)
	_ = mw.WriteField("metadata", `{"main_module":"nested/index.mjs","compatibility_date":"2026-09-28"}`)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="files"; filename="nested/index.mjs"`)
	h.Set("Content-Type", "application/javascript+module")
	part, _ := mw.CreatePart(h)
	_, _ = part.Write([]byte("export default {fetch(){return new Response('ok')}}"))
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/", &raw)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	meta, modules, err := cfReadUpload(req)
	if err != nil || meta.Main != "nested/index.mjs" || len(modules) != 1 || modules[0].Name != meta.Main {
		t.Fatalf("multipart paths lost: %#v %#v %v", meta, modules, err)
	}
}

func TestCloudflareMergePatchBodies(t *testing.T) {
	r := httptest.NewRequest(http.MethodPatch, "/", strings.NewReader(`{"env":{"TOKEN":{"type":"secret_text","text":"fixture"}},"annotations":{"workers/message":"Updated secret"}}`))
	r.Header.Set("Content-Type", "application/merge-patch+json")
	var in struct {
		Env         map[string]*cfBinding `json:"env"`
		Annotations map[string]string     `json:"annotations"`
	}
	if err := cfBody(r, &in); err != nil || in.Env["TOKEN"].Text != "fixture" {
		t.Fatalf("merge patch: %#v %v", in, err)
	}
}

func TestCloudflareDoesNotResurrectNativeDeletedResource(t *testing.T) {
	s, _, base := cfTestServer(t)
	deleted := request(s, "DELETE", "/api/v1/orgs/org-a/workers/fixture", "krun_cli_admin", "")
	if deleted.Code != 204 {
		t.Fatalf("delete: %d %s", deleted.Code, deleted.Body)
	}
	missing := request(s, "GET", base+"/settings", "krun_cli_admin", "")
	if missing.Code != 404 {
		t.Fatalf("deleted locator still resolves: %d %s", missing.Code, missing.Body)
	}
	created := request(s, "POST", cfPrefix+"/accounts/org-a/workers/workers", "krun_cli_admin", `{"name":"fixture"}`)
	if created.Code != 200 || strings.Contains(created.Body.String(), "svc-a") {
		t.Fatalf("expected a fresh Worker identity: %d %s", created.Code, created.Body)
	}
}
