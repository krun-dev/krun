package control

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

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
			_ = store.Update(req.Context(), func(st *State) error {
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
