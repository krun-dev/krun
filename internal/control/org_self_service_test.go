package control

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSelfServiceCreatorOnlyAdministersOwnOrganization(t *testing.T) {
	s, _ := testServer(t)
	s.Config.OrganizationSelfService = true
	response := request(s, "POST", "/api/v1/orgs", "krun_cli_viewer", `{"name":"My team"}`)
	if response.Code != 201 {
		t.Fatal(response.Code, response.Body.String())
	}
	var org Organization
	if err := json.Unmarshal(response.Body.Bytes(), &org); err != nil {
		t.Fatal(err)
	}
	if response = request(s, "GET", "/api/v1/orgs/"+org.ID+"/members", "krun_cli_viewer", ""); response.Code != 200 {
		t.Fatal("creator cannot administer own organization", response.Code, response.Body.String())
	}
	if response = request(s, "GET", "/api/v1/orgs/org-b/members", "krun_cli_viewer", ""); response.Code != 403 {
		t.Fatal("creator gained access to another organization", response.Code, response.Body.String())
	}
	if response = request(s, "GET", "/api/v1/system/users", "krun_cli_viewer", ""); response.Code != 403 {
		t.Fatal("creator gained system privileges", response.Code, response.Body.String())
	}
}

// Self-service must be gated by config, and never available to API keys.
func TestOrganizationSelfServiceCreate(t *testing.T) {
	for _, tc := range []struct {
		name       string
		selfServe  bool
		principal  Principal
		wantStatus int
	}{
		{"disabled blocks regular user", false, Principal{Kind: "session"}, 403},
		{"enabled allows session user", true, Principal{Kind: "session"}, 201},
		{"enabled allows cli session", true, Principal{Kind: "cli_session"}, 201},
		{"enabled rejects api key", true, Principal{Kind: "api_key"}, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, store, _ := workerTestServer(t)
			s.Config.OrganizationSelfService = tc.selfServe
			body := strings.NewReader(`{"name":"self service org"}`)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/orgs", body)
			req.Header.Set("Content-Type", "application/json")
			if tc.principal.Kind == "session" || tc.principal.Kind == "cli_session" {
				tc.principal.User = User{ID: "usr_self", Source: "local", Status: "active", AuthVersion: 1}
			}
			var status int
			_ = store.Update(req.Context(), PartitionAll, func(st *State) error {
				if tc.principal.User.ID != "" {
					st.Users[tc.principal.User.ID] = tc.principal.User
				}
				result, code, err := s.orgCreate(st, tc.principal, req)
				_ = result
				if err != nil {
					if apiErr, ok := err.(*APIError); ok {
						status = apiErr.Status
					} else {
						status = 500
					}
				} else {
					status = code
				}
				return nil
			})
			if status != tc.wantStatus {
				t.Fatalf("got %d, want %d", status, tc.wantStatus)
			}
		})
	}
}
