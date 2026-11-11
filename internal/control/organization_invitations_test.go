package control

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/securetoken"
)

type invitationCreated struct {
	Invitation domain.OrganizationInvitation `json:"invitation"`
	URL        string                        `json:"invite_url"`
}

func invitationTestServer(t *testing.T) (*Server, *Store) {
	t.Helper()
	server, store := testServer(t)
	if err := store.Update(context.Background(), PartitionIAM, func(st *State) error {
		initial, _, _ := fixture()
		initial.Sources = st.Sources
		initial.Bootstrapped = true
		initial.Revision = st.Revision + 1
		*st = *initial
		for i := 0; i < 24; i++ {
			id := fmt.Sprintf("recipient-%d", i)
			st.Users[id] = User{ID: id, Source: "local", Subject: id, Email: id + "@example.com", Status: "active", AuthVersion: 1}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, user := range append([]string{"admin", "viewer"}, recipientIDs(24)...) {
		insertTestSession(t, store, "krun_browser_"+user, Session{User: user, CSRF: "fixture-invitation-csrf"})
	}
	return server, store
}

func recipientIDs(count int) []string {
	ids := make([]string, count)
	for i := range ids {
		ids[i] = fmt.Sprintf("recipient-%d", i)
	}
	return ids
}

func invitationBrowserRequest(s *Server, user, method, path, raw string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(raw))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", s.Config.PublicURL)
	r.Header.Set("X-CSRF-Token", "fixture-invitation-csrf")
	if user != "" {
		r.AddCookie(&http.Cookie{Name: s.cookieName("session"), Value: "krun_browser_" + user})
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func createTestInvitation(t *testing.T, s *Server, input string) invitationCreated {
	t.Helper()
	response := invitationBrowserRequest(s, "admin", "POST", "/api/v1/orgs/org-b/invitation-links", input)
	if response.Code != 201 {
		t.Fatalf("create invitation: %d %s", response.Code, response.Body.String())
	}
	var created invitationCreated
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if response.Header().Get("Cache-Control") != "no-store" || strings.Contains(response.Body.String(), "token_hash") {
		t.Fatal("invitation credential cache or metadata leak")
	}
	return created
}

func invitationBody(created invitationCreated) string {
	token := strings.Split(created.URL, "#invite=")[1]
	return fmt.Sprintf(`{"token":%q}`, token)
}

func TestPostgresOrganizationInvitationHTTP(t *testing.T) { checkOrganizationInvitationHTTP(t) }

func checkOrganizationInvitationHTTP(t *testing.T) {
	s, store := invitationTestServer(t)
	created := createTestInvitation(t, s, `{}`)
	if created.Invitation.Role != "member" || created.Invitation.MaxUses != 1 || !strings.HasPrefix(created.URL, s.Config.PublicURL+"/?page=join#invite=krun_invite_") {
		t.Fatalf("unexpected defaults: %+v", created.Invitation)
	}
	body := invitationBody(created)
	preview := invitationBrowserRequest(s, "", "POST", "/api/v1/invitation-links/preview", body)
	if preview.Code != 200 || strings.Contains(preview.Body.String(), "creator") || strings.Contains(preview.Body.String(), "target_email") || preview.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("public preview: %d %s", preview.Code, preview.Body.String())
	}
	for _, check := range []struct {
		user, method, path, raw string
		status                  int
	}{
		{"", "POST", "/api/v1/invitation-links/accept", body, 401},
		{"viewer", "POST", "/api/v1/orgs/org-a/invitation-links", `{}`, 403},
		{"admin", "POST", "/api/v1/orgs/org-b/invitation-links", `{"role_id":"org_admin"}`, 422},
		{"admin", "POST", "/api/v1/orgs/org-b/invitation-links", `{"max_uses":0}`, 422},
		{"admin", "POST", "/api/v1/orgs/org-b/invitation-links", `{"max_uses":101}`, 422},
		{"admin", "POST", "/api/v1/orgs/org-b/invitation-links", `{"expires_in":3600}`, 400},
		{"admin", "DELETE", "/api/v1/orgs/org-a/invitation-links/" + created.Invitation.ID, "", 404},
	} {
		response := invitationBrowserRequest(s, check.user, check.method, check.path, check.raw)
		if response.Code != check.status {
			t.Errorf("%s %s: got %d, want %d: %s", check.method, check.path, response.Code, check.status, response.Body.String())
		}
	}
	r := httptest.NewRequest("POST", "/api/v1/invitation-links/accept", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", s.Config.PublicURL)
	r.AddCookie(&http.Cookie{Name: s.cookieName("session"), Value: "krun_browser_recipient-0"})
	badCSRF := httptest.NewRecorder()
	s.ServeHTTP(badCSRF, r)
	if badCSRF.Code != 403 {
		t.Fatal("accept allowed without CSRF", badCSRF.Code)
	}
	for attempt := 0; attempt < 2; attempt++ {
		response := invitationBrowserRequest(s, "recipient-0", "POST", "/api/v1/invitation-links/accept", body)
		if response.Code != 200 || !strings.Contains(response.Body.String(), `"can_enter":true`) {
			t.Fatalf("accept/retry: %d %s", response.Code, response.Body.String())
		}
	}
	response := invitationBrowserRequest(s, "recipient-1", "POST", "/api/v1/invitation-links/accept", body)
	if response.Code != 410 {
		t.Fatalf("full invitation accepted another user: %d %s", response.Code, response.Body.String())
	}
	current, _, err := store.OrganizationInvitation(context.Background(), created.Invitation.ID, "")
	if err != nil || current.UseCount != 1 || current.Status != "exhausted" {
		t.Fatalf("incorrect quota: %+v %v", current, err)
	}
	if err := store.Update(context.Background(), PartitionIAM, func(st *State) error {
		return s.iam().DeleteMember(st, Target{Kind: "organization", ID: "org-b", Organization: "org-b"}, "recipient-0")
	}); err != nil {
		t.Fatal(err)
	}
	response = invitationBrowserRequest(s, "recipient-0", "POST", "/api/v1/invitation-links/accept", body)
	if response.Code != 409 || !strings.Contains(response.Body.String(), "INVITATION_ALREADY_USED") {
		t.Fatalf("removed user reused invitation: %d %s", response.Code, response.Body.String())
	}
	for attempt := 0; attempt < 2; attempt++ {
		response = invitationBrowserRequest(s, "admin", "DELETE", "/api/v1/orgs/org-b/invitation-links/"+created.Invitation.ID, "")
		if response.Code != 204 {
			t.Fatal("revoke not idempotent", response.Code, response.Body.String())
		}
	}
	response = invitationBrowserRequest(s, "", "POST", "/api/v1/invitation-links/preview", body)
	if response.Code != 410 {
		t.Fatal("revoked link remained available", response.Code)
	}
}

func TestPostgresOrganizationInvitationQuotaConcurrency(t *testing.T) {
	s, store := invitationTestServer(t)
	created := createTestInvitation(t, s, `{"max_uses":5}`)
	var joined atomic.Int32
	errors := make(chan string, 24)
	var group sync.WaitGroup
	start := make(chan struct{})
	for _, user := range recipientIDs(24) {
		group.Add(1)
		go func(user string) {
			defer group.Done()
			<-start
			for attempt := 0; attempt < 3; attempt++ {
				response := invitationBrowserRequest(s, user, "POST", "/api/v1/invitation-links/accept", invitationBody(created))
				if response.Code == 409 {
					continue
				}
				if response.Code == 200 {
					joined.Add(1)
				} else if response.Code != 410 {
					errors <- fmt.Sprintf("%d %s", response.Code, response.Body.String())
				}
				return
			}
			errors <- "invitation did not converge after retries"
		}(user)
	}
	close(start)
	group.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
	current, _, err := store.OrganizationInvitation(context.Background(), created.Invitation.ID, "")
	if err != nil || current.UseCount != 5 || joined.Load() != 5 {
		t.Fatalf("quota over/under-count: joined=%d invitation=%+v err=%v", joined.Load(), current, err)
	}
	if err := store.View(context.Background(), PartitionIAM, func(st *State) error {
		members := 0
		roles := 0
		for _, user := range recipientIDs(24) {
			if _, exists := st.Members[memberKey("org-b", user)]; exists {
				members++
			}
		}
		for _, role := range st.Assignments {
			if role.ScopeID == "org-b" && role.Role == "member" {
				roles++
			}
		}
		if members != 5 || roles != 5 {
			t.Errorf("partial invitation writes: members=%d roles=%d", members, roles)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresOrganizationInvitationRollback(t *testing.T) {
	s, store := invitationTestServer(t)
	created := createTestInvitation(t, s, `{}`)
	in, _, err := store.OrganizationInvitation(context.Background(), created.Invitation.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.OrganizationInvitationContext(context.Background(), "org-b", "admin", "recipient-0")
	if err != nil {
		t.Fatal(err)
	}
	assignment := Assignment{ID: "admin", SubjectKind: "user", SubjectID: "recipient-0", Role: "member", ScopeKind: "organization", ScopeID: "org-b", Revision: 1}
	_, err = store.RedeemOrganizationInvitation(context.Background(), in, st.Users["recipient-0"], assignment, domain.Audit{ID: securetoken.NewID("audit"), Time: now()})
	if err == nil {
		t.Fatal("conflicting role ID did not roll back")
	}
	current, _, err := store.OrganizationInvitation(context.Background(), in.ID, "")
	if err != nil || current.UseCount != 0 {
		t.Fatal("failed role write consumed a place", current, err)
	}
	redeemed, err := store.OrganizationInvitationRedeemed(context.Background(), in.ID, "recipient-0")
	if err != nil || redeemed {
		t.Fatal("failed transaction left a redemption", redeemed, err)
	}
	if err := store.View(context.Background(), PartitionIAM, func(st *State) error {
		if _, found := st.Members[memberKey("org-b", "recipient-0")]; found {
			t.Error("failed transaction left a membership")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresOrganizationInvitationRecipients(t *testing.T) {
	checkOrganizationInvitationRecipients(t)
}

func checkOrganizationInvitationRecipients(t *testing.T) {
	s, store := invitationTestServer(t)
	created := createTestInvitation(t, s, `{"target_user_id":"recipient-0","role_id":"viewer"}`)
	for _, user := range []string{"recipient-1", "recipient-0"} {
		response := invitationBrowserRequest(s, user, "POST", "/api/v1/invitation-links/accept", invitationBody(created))
		expected := 403
		if user == "recipient-0" {
			expected = 200
		}
		if response.Code != expected {
			t.Fatalf("directed user: %d %s", response.Code, response.Body.String())
		}
	}
	created = createTestInvitation(t, s, `{"target_email":"Recipient-1@EXAMPLE.com"}`)
	preview := invitationBrowserRequest(s, "", "POST", "/api/v1/invitation-links/preview", invitationBody(created))
	if preview.Code != 200 || !strings.Contains(preview.Body.String(), `"targeted":true`) || strings.Contains(preview.Body.String(), "recipient-1") {
		t.Fatal("directed recipient leaked in preview", preview.Body.String())
	}
	response := invitationBrowserRequest(s, "recipient-1", "POST", "/api/v1/invitation-links/accept", invitationBody(created))
	if response.Code != 403 {
		t.Fatal("unverified email accepted", response.Code, response.Body.String())
	}
	if err := store.Update(context.Background(), PartitionIAM, func(st *State) error {
		user := st.Users["recipient-1"]
		user.EmailVerified = true
		st.Users[user.ID] = user
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	response = invitationBrowserRequest(s, "recipient-1", "POST", "/api/v1/invitation-links/accept", invitationBody(created))
	if response.Code != 200 {
		t.Fatal("verified email rejected", response.Code, response.Body.String())
	}
	// An existing viewer cannot gain member through another invitation.
	existing := createTestInvitation(t, s, `{}`)
	response = invitationBrowserRequest(s, "recipient-0", "POST", "/api/v1/invitation-links/accept", invitationBody(existing))
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"already_member"`) {
		t.Fatal("existing member response", response.Code, response.Body.String())
	}
	current, _, err := store.OrganizationInvitation(context.Background(), existing.Invitation.ID, "")
	if err != nil || current.UseCount != 0 {
		t.Fatal("existing member consumed quota", current, err)
	}
	if err := store.View(context.Background(), PartitionIAM, func(st *State) error {
		roles := []string{}
		for _, assignment := range st.Assignments {
			if assignment.SubjectID == "recipient-0" && assignment.ScopeID == "org-b" {
				roles = append(roles, assignment.Role)
			}
		}
		if len(roles) != 1 || roles[0] != "viewer" {
			t.Errorf("existing role changed: %v", roles)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresOrganizationInvitationSameUserConcurrency(t *testing.T) {
	for _, crossLink := range []bool{false, true} {
		t.Run(fmt.Sprintf("cross-link=%t", crossLink), func(t *testing.T) {
			s, store := invitationTestServer(t)
			first := createTestInvitation(t, s, `{"max_uses":10}`)
			second := first
			if crossLink {
				second = createTestInvitation(t, s, `{"max_uses":10,"role_id":"viewer"}`)
			}
			var group sync.WaitGroup
			start := make(chan struct{})
			for i := 0; i < 12; i++ {
				created := first
				if i%2 == 1 {
					created = second
				}
				group.Add(1)
				go func() {
					defer group.Done()
					<-start
					response := invitationBrowserRequest(s, "recipient-0", "POST", "/api/v1/invitation-links/accept", invitationBody(created))
					if response.Code != 200 {
						t.Errorf("same user retry: %d %s", response.Code, response.Body.String())
					}
				}()
			}
			close(start)
			group.Wait()
			items, err := store.ListOrganizationInvitations(context.Background(), "org-b", "", 100)
			if err != nil {
				t.Fatal(err)
			}
			uses := 0
			for _, item := range items {
				uses += item.UseCount
			}
			if uses != 1 {
				t.Fatalf("one user consumed %d places", uses)
			}
		})
	}
}

func TestPostgresOrganizationInvitationRevokeConcurrency(t *testing.T) {
	s, store := invitationTestServer(t)
	created := createTestInvitation(t, s, `{}`)
	start := make(chan struct{})
	response := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		<-start
		response <- invitationBrowserRequest(s, "recipient-0", "POST", "/api/v1/invitation-links/accept", invitationBody(created))
	}()
	close(start)
	revoked := invitationBrowserRequest(s, "admin", "DELETE", "/api/v1/orgs/org-b/invitation-links/"+created.Invitation.ID, "")
	accepted := <-response
	if revoked.Code != 204 || accepted.Code != 200 && accepted.Code != 410 {
		t.Fatal("revoke/accept race", revoked.Code, accepted.Code, accepted.Body.String())
	}
	current, _, err := store.OrganizationInvitation(context.Background(), created.Invitation.ID, "")
	expected := 0
	if accepted.Code == 200 {
		expected = 1
	}
	if err != nil || current.UseCount != expected || current.Revoked == nil {
		t.Fatal("revoke/accept inconsistent", current, err)
	}
	later := invitationBrowserRequest(s, "recipient-1", "POST", "/api/v1/invitation-links/accept", invitationBody(created))
	if later.Code != 410 {
		t.Fatal("accepted after revocation", later.Code)
	}
}
