package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestManagementRoutes(t *testing.T) {
	inputFile := filepath.Join(t.TempDir(), "group.yaml")
	if err := os.WriteFile(inputFile, []byte("name: Renamed\nexpected_revision: 2\n"), 0600); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name   string
		args   []string
		method string
		path   string
		body   string
	}{
		{
			name:   "organization create",
			args:   []string{"org", "create", "--name", "Team", "--short-name", "team"},
			method: http.MethodPost,
			path:   "/api/v1/orgs",
			body:   `{"name":"Team","short_name":"team"}`,
		},
		{
			name:   "remove member with escaped ID",
			args:   []string{"members", "remove", "--org", "team", "--user", "user/a"},
			method: http.MethodDelete,
			path:   "/api/v1/orgs/team/members/user%2Fa",
		},
		{
			name:   "group update from file",
			args:   []string{"groups", "update", "--org", "team", "--id", "group-a", "--file", inputFile},
			method: http.MethodPatch,
			path:   "/api/v1/orgs/team/groups/group-a",
			body:   `{"name":"Renamed","expected_revision":2}`,
		},
		{
			name:   "add group member",
			args:   []string{"groups", "add-member", "--org", "team", "--group", "group-a", "--user", "user-a"},
			method: http.MethodPut,
			path:   "/api/v1/orgs/team/groups/group-a/members/user-a",
		},
		{
			name:   "system role update",
			args:   []string{"roles", "update", "--system", "--id", "assignment-a", "--subject", "user-a", "--role", "system_admin", "--revision", "3"},
			method: http.MethodPut,
			path:   "/api/v1/system/role-assignments/assignment-a",
			body:   `{"subject_kind":"user","subject_id":"user-a","role_id":"system_admin","scope_kind":"system","scope_id":"system","conditions":{"environments":[]},"expected_revision":3}`,
		},
		{
			name:   "key show without organization",
			args:   []string{"keys", "show", "--id", "key-a"},
			method: http.MethodGet,
			path:   "/api/v1/api-keys/key-a",
		},
		{
			name:   "model locator supplies organization",
			args:   []string{"models", "get", "--id", "team/models/image"},
			method: http.MethodGet,
			path:   "/api/v1/orgs/team/models/image",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != tc.method || r.URL.EscapedPath() != tc.path {
					t.Errorf("request %s %s, want %s %s", r.Method, r.URL.EscapedPath(), tc.method, tc.path)
				}
				raw, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				if tc.body == "" {
					if len(raw) != 0 {
						t.Errorf("unexpected body %s", raw)
					}
				} else {
					var want, got any
					if json.Unmarshal([]byte(tc.body), &want) != nil ||
						json.Unmarshal(raw, &got) != nil || !reflect.DeepEqual(want, got) {
						t.Errorf("body %s, want %s", raw, tc.body)
					}
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			defer server.Close()

			client := Client{Profile: Profile{Endpoint: server.URL}, Out: io.Discard, Err: io.Discard}
			var err error
			if tc.args[0] == "org" {
				err = client.organization(t.Context(), tc.args[1:])
			} else {
				err = client.manage(t.Context(), tc.args[0], tc.args[1:])
			}
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}
