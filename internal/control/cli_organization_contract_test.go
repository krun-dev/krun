package control

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	kruncli "github.com/krun-dev/krun/internal/cli"
	appconfig "github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/configfile"
	"github.com/krun-dev/krun/internal/domain"
)

func TestCLIOrganizationManagementContract(t *testing.T) {
	checkCLIOrganizationManagement(t, postgresTestStore(t))
}

func checkCLIOrganizationManagement(t *testing.T, store *Store) {
	t.Helper()
	ctx := context.Background()
	cfg := appconfig.Config{
		DB:          "postgres://test-only/krun_test",
		Development: true,
		PublicURL:   "http://127.0.0.1:8080",
		Providers: []appconfig.ProviderConfig{
			{ID: "local", Source: "local", Type: "dev", Registration: "jit"},
			{ID: "alternate", Source: "alternate", Type: "dev", ClientID: "replace-me-alternate", Registration: "jit"},
		},
	}
	server, err := NewServer(ctx, cfg, store)
	if err != nil {
		t.Fatal(err)
	}

	fixtures, admin, viewer := fixture()
	viewer.Source = "alternate"
	if err := store.Update(ctx, PartitionIAM, func(st *State) error {
		st.Bootstrapped = true
		st.Users[admin.ID] = admin
		st.Users[viewer.ID] = viewer
		st.Assignments["admin"] = fixtures.Assignments["admin"]
		st.Revision++
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	httpServer := httptest.NewServer(server)
	t.Cleanup(httpServer.Close)
	server.Config.PublicURL = httpServer.URL
	t.Setenv("KRUN_TOKEN", "")
	t.Setenv("KRUN_ENDPOINT", "")
	profiles := map[string]string{}
	for _, user := range []User{admin, viewer} {
		token := "krun_cli_" + user.ID
		session := Session{
			User: user.ID, AuthVersion: user.AuthVersion, CLI: true,
			Created: now(), Seen: now(), Absolute: now().Add(time.Hour),
		}
		if err := store.InsertSession(ctx, token, session); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(t.TempDir(), "profile.yaml")
		profile := kruncli.Profile{Endpoint: httpServer.URL, Token: token, Organization: "cli-team"}
		data, err := configfile.Marshal(path, profile)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		profiles[user.ID] = path
	}

	run := func(user User, output any, args ...string) error {
		var stdout, stderr bytes.Buffer
		args = append(args, "--profile", profiles[user.ID])
		if err := kruncli.Run(ctx, args, strings.NewReader(""), &stdout, &stderr); err != nil {
			return err
		}
		if output != nil {
			return json.Unmarshal(stdout.Bytes(), output)
		}
		return nil
	}
	mustRun := func(user User, output any, args ...string) {
		t.Helper()
		if err := run(user, output, args...); err != nil {
			t.Fatalf("CLI %v: %v", args, err)
		}
	}
	wantError := func(user User, code string, args ...string) {
		t.Helper()
		if err := run(user, nil, args...); err == nil || !strings.HasPrefix(err.Error(), code+":") {
			t.Fatalf("CLI %v: got %v, want %s", args, err, code)
		}
	}

	var created Organization
	mustRun(admin, &created, "org", "create", "--name", "CLI team", "--short-name", "cli-team")
	var current Organization
	mustRun(admin, &current, "org", "get", "--org", created.ID)
	if current.ID != created.ID || current.ShortName != "cli-team" || current.Name != "CLI team" {
		t.Fatalf("unexpected organization: %+v", current)
	}
	var permissions struct {
		Organization string          `json:"organization_id"`
		Actions      map[string]bool `json:"actions"`
	}
	mustRun(admin, &permissions, "org", "permissions")
	if permissions.Organization != created.ID || !permissions.Actions["organization.update"] {
		t.Fatalf("unexpected permissions: %+v", permissions)
	}
	wantError(viewer, "RESOURCE_NOT_FOUND", "org", "permissions")

	sources := []string{"local", "alternate"}
	mustRun(admin, &current, "org", "update", "--allowed-source", sources[0], "--allowed-source", sources[1])
	if current.Name != created.Name || !slices.Equal(current.Sources, sources) {
		t.Fatalf("source-only update changed the name or lost sources: %+v", current)
	}
	mustRun(admin, &current, "org", "update", "--name", "Renamed CLI team")
	slices.Sort(current.Sources)
	if current.Name != "Renamed CLI team" || !slices.Equal(current.Sources, []string{"alternate", "local"}) {
		t.Fatalf("name-only update changed the source list: %+v", current)
	}
	wantError(admin, "REVISION_CONFLICT", "org", "update", "--name", "Stale name", "--revision", strconv.Itoa(created.Revision))

	var group Group
	mustRun(admin, &group, "groups", "create", "--name", "Old group")
	oldRevision := strconv.Itoa(group.Revision)
	mustRun(admin, &group, "groups", "update", "--id", group.ID, "--name", "New group", "--revision", oldRevision)
	if group.Name != "New group" {
		t.Fatalf("group rename failed: %+v", group)
	}
	wantError(admin, "REVISION_CONFLICT", "groups", "update", "--id", group.ID, "--name", "Stale group", "--revision", oldRevision)

	var shared, directed invitationCreated
	mustRun(admin, &shared, "org", "invitations", "create")
	mustRun(admin, &directed, "org", "invitations", "create",
		"--role", "viewer", "--max-uses", "2", "--target-user", viewer.ID)
	if shared.Invitation.Role != "member" || shared.Invitation.MaxUses != 1 ||
		directed.Invitation.Role != "viewer" || directed.Invitation.MaxUses != 2 ||
		directed.Invitation.TargetUser != viewer.ID || !strings.HasPrefix(directed.URL, httpServer.URL+"/?page=join#invite=") {
		t.Fatal("invitation defaults, recipient, or link do not match the CLI request")
	}
	var firstPage, nextPage struct {
		Items      []domain.OrganizationInvitation `json:"items"`
		NextCursor string                          `json:"next_cursor"`
	}
	mustRun(admin, &firstPage, "org", "invitations", "list", "--limit", "1")
	if len(firstPage.Items) != 1 || firstPage.NextCursor == "" {
		t.Fatalf("unexpected first invitation page: %+v", firstPage)
	}
	mustRun(admin, &nextPage, "org", "invitations", "list", "--limit", "1", "--cursor", firstPage.NextCursor)
	if len(nextPage.Items) != 1 || nextPage.NextCursor != "" || nextPage.Items[0].ID == firstPage.Items[0].ID {
		t.Fatalf("unexpected next invitation page: %+v", nextPage)
	}
	for range 2 {
		mustRun(admin, nil, "org", "invitations", "revoke", "--id", directed.Invitation.ID)
	}
	var rawList json.RawMessage
	mustRun(admin, &rawList, "org", "invitations", "list")
	if bytes.Contains(rawList, []byte("invite_url")) || bytes.Contains(rawList, []byte("krun_invite_")) {
		t.Fatal("invitation list exposed credentials")
	}
	if err := json.Unmarshal(rawList, &firstPage); err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(firstPage.Items, func(invitation domain.OrganizationInvitation) bool {
		return invitation.ID == directed.Invitation.ID && invitation.Status == "revoked"
	}) {
		t.Fatal("revoked invitation status missing from list")
	}
	var email invitationCreated
	mustRun(admin, &email, "org", "invitations", "create", "--target-email", "Teammate@example.com")
	if email.Invitation.TargetEmail != "teammate@example.com" {
		t.Fatal("invitation email was not saved")
	}

	wantError(admin, "LAST_ORG_ADMIN", "org", "update", "--allowed-source", viewer.Source)
	mustRun(admin, nil, "members", "add", "--user", viewer.ID)
	mustRun(admin, nil, "roles", "grant", "--subject", viewer.ID, "--role", "org_admin")
	mustRun(admin, &current, "org", "update", "--allowed-source", viewer.Source)
	if !slices.Equal(current.Sources, []string{viewer.Source}) {
		t.Fatalf("handoff did not allow source update: %+v", current)
	}
	mustRun(viewer, &current, "org", "update", "--clear-allowed-sources")
	if len(current.Sources) != 0 || current.Name != "Renamed CLI team" {
		t.Fatalf("clear source list changed other settings: %+v", current)
	}
}
