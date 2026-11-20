package control

import (
	"net/http"
	"strings"
	"testing"
)

func TestWorkerNamesAndPreviewAddresses(t *testing.T) {
	s, _, _ := workerTestServer(t)
	base := "/api/v1/orgs/org-a/workers"
	if r := request(s, http.MethodPost, base, "krun_cli_admin", `{"name":"orders"}`); r.Code != http.StatusCreated {
		t.Fatalf("create Worker: %d %s", r.Code, r.Body.String())
	}
	if r := request(s, http.MethodPost, "/api/v1/orgs/org-b/workers", "krun_cli_admin", `{"name":"orders"}`); r.Code != http.StatusConflict {
		t.Fatalf("cross-organization name collision: %d %s", r.Code, r.Body.String())
	}
	if r := request(s, http.MethodPost, base, "krun_cli_admin", `{"name":"invalid_name"}`); r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid DNS name: %d %s", r.Code, r.Body.String())
	}
	if r := request(s, http.MethodPost, base+"/svc-a/environments", "krun_cli_admin", `{"name":"staging"}`); r.Code != http.StatusCreated || !strings.Contains(r.Body.String(), "staging-svc-a.") {
		t.Fatalf("create Preview: %d %s", r.Code, r.Body.String())
	}
	if r := request(s, http.MethodPost, base, "krun_cli_admin", `{"name":"staging-svc-a"}`); r.Code != http.StatusConflict {
		t.Fatalf("Preview hostname collision: %d %s", r.Code, r.Body.String())
	}
	if r := request(s, http.MethodPatch, base+"/orders", "krun_cli_admin", `{"name":"renamed","expected_revision":1}`); r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("Worker rename: %d %s", r.Code, r.Body.String())
	}
}
