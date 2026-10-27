package control

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func memoryStore() *Store { return &Store{memory: newState()} }

func fixture() (*State, User, User) {
	st := newState()
	admin := User{ID: "admin", Source: "local", Subject: "a", Status: "active", AuthVersion: 1}
	viewer := User{ID: "viewer", Source: "local", Subject: "b", Status: "active", AuthVersion: 1}
	st.Users[admin.ID] = admin
	st.Users[viewer.ID] = viewer
	for _, id := range []string{"org-a", "org-b"} {
		st.Organizations[id] = Organization{ID: id, Name: id}
		st.Members[memberKey(id, admin.ID)] = Member{id, admin.ID}
	}
	st.Members[memberKey("org-a", viewer.ID)] = Member{"org-a", viewer.ID}
	st.Assignments["admin"] = Assignment{ID: "admin", SubjectKind: "user", SubjectID: admin.ID, Role: "system_admin", ScopeKind: "system", ScopeID: "system"}
	st.Assignments["viewer"] = Assignment{ID: "viewer", SubjectKind: "user", SubjectID: viewer.ID, Role: "viewer", ScopeKind: "organization", ScopeID: "org-a"}
	st.Resources["svc-a"] = Resource{ID: "svc-a", Kind: "service", Organization: "org-a", Name: "A", Revision: 1}
	st.Resources["svc-b"] = Resource{ID: "svc-b", Kind: "service", Organization: "org-b", Name: "B", Revision: 1}
	return st, admin, viewer
}
func TestAuthorizationBoundaries(t *testing.T) {
	st, admin, viewer := fixture()
	tests := []struct {
		name, user, action, kind, id, environment string
		allow                                     bool
	}{{"system admin cross organization", "admin", "service.read", "service", "svc-b", "global", true}, {"viewer reads own organization", "viewer", "service.read", "service", "svc-a", "global", true}, {"cross organization denied", "viewer", "service.read", "service", "svc-b", "global", false}, {"viewer cannot publish", "viewer", "service.publish", "service", "svc-a", "production", false}, {"unknown action denied", "admin", "service.magic", "service", "svc-a", "global", false}, {"action kind mismatch denied", "admin", "model.read", "service", "svc-a", "global", false}, {"unknown environment denied", "admin", "service.read", "service", "svc-a", "bad env!", false}}
	_ = admin
	_ = viewer
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			target, e := resolve(st, tc.kind, tc.id, tc.environment)
			if e != nil {
				t.Fatal(e)
			}
			d, e := Authorize(st, Principal{User: st.Users[tc.user]}, tc.action, target)
			if e != nil {
				t.Fatal(e)
			}
			if d.Allowed != tc.allow {
				t.Fatalf("decision %+v want %t", d, tc.allow)
			}
		})
	}
}
func TestKeyIntersectionExplicitDenyAndRevocation(t *testing.T) {
	st, admin, viewer := fixture()
	policy := Policy{Version: 1, Statements: []Statement{{Effect: "allow", Actions: []string{"service.read", "service.publish"}, Resource: Selector{Organization: "org-a", Kind: "service", IDs: []string{"svc-a"}}, Conditions: Conditions{Environments: []string{"release-candidate"}}}, {Effect: "deny", Actions: []string{"service.publish"}, Resource: Selector{Organization: "org-a", Kind: "service", IDs: []string{"svc-a"}}}}}
	key := APIKey{ID: "key-a", ScopeKind: "organization", Organization: "org-a", Owner: admin.ID, Policy: policy}
	target, _ := resolve(st, "service", "svc-a", "release-candidate")
	p := Principal{User: admin, Key: &key}
	d, e := Authorize(st, p, "service.read", target)
	if e != nil || !d.Allowed {
		t.Fatalf("%+v %v", d, e)
	}
	d, e = Authorize(st, p, "service.publish", target)
	if e != nil || d.Allowed {
		t.Fatalf("explicit deny ignored %+v %v", d, e)
	}
	target.Environment = "production"
	d, e = Authorize(st, p, "service.read", target)
	if e != nil || d.Allowed {
		t.Fatal("environment constraint ignored")
	}
	target.Environment = "release-candidate"
	p.User = viewer
	delete(st.Assignments, "viewer")
	d, e = Authorize(st, p, "service.read", target)
	if e != nil || d.Allowed || !d.KeyAllowed {
		t.Fatalf("key bypassed current user permissions %+v %v", d, e)
	}
	target, _ = resolve(st, "service", "svc-b", "release-candidate")
	p.User = admin
	d, e = Authorize(st, p, "service.read", target)
	if e != nil || d.Allowed {
		t.Fatal("key escaped organization")
	}
}
func TestGroupsAndIndependentSources(t *testing.T) {
	st, _, viewer := fixture()
	delete(st.Assignments, "viewer")
	st.Groups["publishers"] = Group{ID: "publishers", ScopeKind: "organization", ScopeID: "org-a", Members: []string{viewer.ID}}
	st.Assignments["publish"] = Assignment{ID: "publish", SubjectKind: "group", SubjectID: "publishers", Role: "publisher", ScopeKind: "service", ScopeID: "svc-a", Conditions: Conditions{Environments: []string{"release-candidate"}}}
	target, _ := resolve(st, "service", "svc-a", "release-candidate")
	d, e := Authorize(st, Principal{User: viewer}, "service.publish", target)
	if e != nil || !d.Allowed {
		t.Fatalf("group scoped role failed %+v %v", d, e)
	}
	g := st.Groups["publishers"]
	g.Members = nil
	st.Groups[g.ID] = g
	d, e = Authorize(st, Principal{User: viewer}, "service.publish", target)
	if e != nil || d.Allowed {
		t.Fatal("removed group membership still authorized")
	}
	g.Members = []string{viewer.ID}
	st.Groups[g.ID] = g
	org := st.Organizations["org-a"]
	org.Sources = []string{"company"}
	st.Organizations[org.ID] = org
	d, e = Authorize(st, Principal{User: viewer}, "service.publish", target)
	if e != nil || d.Allowed {
		t.Fatal("organization source gate bypassed")
	}
	other := viewer
	other.ID = "same-email-new-source"
	other.Source = "company"
	st.Users[other.ID] = other
	d, e = Authorize(st, Principal{User: other}, "service.publish", target)
	if e != nil || d.Allowed {
		t.Fatal("new source inherited permissions")
	}
}
func TestPolicyDelegation(t *testing.T) {
	st, admin, viewer := fixture()
	tests := []struct {
		name   string
		p      Principal
		policy Policy
		ok     bool
	}{{"exact read within owner rights", Principal{User: viewer}, Policy{Version: 1, Statements: []Statement{{Effect: "allow", Actions: []string{"service.read"}, Resource: Selector{Kind: "service", Organization: "org-a", IDs: []string{"svc-a"}}}}}, true}, {"viewer can restrict a publish template", Principal{User: viewer}, Policy{Version: 1, Statements: []Statement{{Effect: "allow", Actions: []string{"service.publish"}, Resource: Selector{Kind: "service", Organization: "org-a", IDs: []string{"svc-a"}}}}}, true}, {"ordinary user organization wildcard", Principal{User: viewer}, Policy{Version: 1, Statements: []Statement{{Effect: "allow", Actions: []string{"service.read"}, Resource: Selector{Kind: "service", Organization: "org-a", All: true}}}}, true}, {"admin organization wildcard", Principal{User: admin}, Policy{Version: 1, Statements: []Statement{{Effect: "allow", Actions: []string{"service.read"}, Resource: Selector{Kind: "service", Organization: "org-a", All: true}}}}, true}, {"cross organization selector rejected", Principal{User: admin}, Policy{Version: 1, Statements: []Statement{{Effect: "allow", Actions: []string{"service.read"}, Resource: Selector{Kind: "service", Organization: "org-a", IDs: []string{"svc-b"}}}}}, false}, {"empty policy", Principal{User: admin}, Policy{Version: 1}, false}}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := validatePolicy(st, tc.p, "org-a", "organization", tc.policy)
			if (e == nil) != tc.ok {
				t.Fatalf("error=%v want valid=%t", e, tc.ok)
			}
		})
	}
}
func testServer(t *testing.T) (*Server, *Store) {
	t.Helper()
	store := memoryStore()
	c := Config{DB: "postgres://test-only/krun_test", Development: true, PublicURL: "http://127.0.0.1:8080", Providers: []ProviderConfig{{ID: "local", Source: "local", Type: "dev", Registration: "jit"}}}
	s, e := NewServer(context.Background(), c, store)
	if e != nil {
		t.Fatal(e)
	}
	st, admin, viewer := fixture()
	st.Bootstrapped = true
	st.Sources["local"] = fingerprint(c.Providers[0])
	for _, u := range []User{admin, viewer} {
		token := "krun_cli_" + u.ID
		st.Sessions[digest(token)] = Session{User: u.ID, AuthVersion: 1, CLI: true, Created: now(), Seen: now(), Absolute: now().Add(time.Hour)}
	}
	if e = store.Update(context.Background(), PartitionAll, func(state *State) error { *state = *st; return nil }); e != nil {
		t.Fatal(e)
	}
	return s, store
}
func request(s *Server, method, path, token, raw string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(raw))
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func TestAPIValidationAndLastAdmin(t *testing.T) {
	s, store := testServer(t)
	tests := []struct {
		method, path, token, raw string
		status                   int
	}{{"GET", "/api/v1/orgs", "", "", 401}, {"GET", "/api/v1/orgs/org-b/members", "krun_cli_viewer", "", 403}, {"POST", "/api/v1/orgs/org-a/role-assignments", "krun_cli_admin", `{"subject_kind":"user","subject_id":"viewer","role_id":"system_admin","scope_kind":"system","scope_id":"system","conditions":{}}`, 422}, {"POST", "/api/v1/orgs", "krun_cli_admin", `{"name":"A","name":"B"}`, 400}, {"POST", "/api/v1/orgs", "krun_cli_admin", `{"name":"A","owner_user_id":"viewer"}`, 400}, {"PATCH", "/api/v1/system/users/admin", "krun_cli_admin", `{"status":"disabled"}`, 409}, {"DELETE", "/api/v1/system/role-assignments/admin", "krun_cli_admin", "", 409}, {"POST", "/api/v1/authz/check", "krun_cli_admin", `{"action":"service.read","resource_kind":"service","resource_id":"svc-a","principal_id":"admin"}`, 400}, {"GET", "/api/v1/orgs/org-b/services/svc-a", "krun_cli_admin", "", 404}}
	for _, tc := range tests {
		t.Run(tc.path+tc.method+tc.raw, func(t *testing.T) {
			w := request(s, tc.method, tc.path, tc.token, tc.raw)
			if w.Code != tc.status {
				t.Fatalf("got %d want %d: %s", w.Code, tc.status, w.Body.String())
			}
		})
	}
	_ = store.Update(context.Background(), PartitionAll, func(st *State) error {
		if st.Users["admin"].Status != "active" || st.Assignments["admin"].ID == "" {
			t.Error("failed operation committed changes")
		}
		return nil
	})
}
func TestAPIKeyLifecycleAndImmediateUserRevocation(t *testing.T) {
	s, store := testServer(t)
	raw := `{"name":"test","expires_in":3600,"policy":{"schema_version":1,"statements":[{"effect":"allow","actions":["service.read"],"resource":{"organization_id":"org-a","kind":"service","ids":["svc-a"]},"conditions":{}}]}}`
	w := request(s, "POST", "/api/v1/orgs/org-a/api-keys", "krun_cli_viewer", raw)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	var issued struct{ ID, Key string }
	if e := json.Unmarshal(w.Body.Bytes(), &issued); e != nil {
		t.Fatal(e)
	}
	if request(s, "GET", "/api/v1/orgs/org-a/services/svc-a", issued.Key, "").Code != 200 {
		t.Fatal("exact key cannot read")
	}
	if request(s, "GET", "/api/v1/orgs/org-b/services/svc-b", issued.Key, "").Code != 404 {
		t.Fatal("key escaped organization")
	}
	if request(s, "POST", "/api/v1/orgs/org-a/groups", issued.Key, `{"name":"x"}`).Code != 403 {
		t.Fatal("API key allowed IAM mutation")
	}
	w = request(s, "GET", "/api/v1/orgs/org-a/api-keys", "krun_cli_viewer", "")
	if strings.Contains(w.Body.String(), issued.Key) {
		t.Fatal("secret leaked in metadata")
	}
	_ = store.Update(context.Background(), PartitionAll, func(st *State) error { delete(st.Assignments, "viewer"); return nil })
	if request(s, "GET", "/api/v1/orgs/org-a/services/svc-a", issued.Key, "").Code != 404 {
		t.Fatal("removed role remained active")
	}
	_ = store.Update(context.Background(), PartitionAll, func(st *State) error { k := st.Keys[issued.ID]; k.Revoked = true; st.Keys[k.ID] = k; return nil })
	if request(s, "GET", "/api/v1/me", issued.Key, "").Code != 401 {
		t.Fatal("revoked key remained valid")
	}
}
func browserRequest(s *Server, method, path string, form url.Values, cookies []*http.Cookie) *httptest.ResponseRecorder {
	var raw string
	if form != nil {
		raw = form.Encode()
	}
	r := httptest.NewRequest(method, path, strings.NewReader(raw))
	r.Header.Set("Origin", s.Config.PublicURL)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func TestLoginCSRFReplayLogoutAndStableIdentity(t *testing.T) {
	s, store := testServer(t)
	csrfResponse := browserRequest(s, "GET", "/api/auth/csrf", nil, nil)
	var csrf struct {
		Token string `json:"csrf_token"`
	}
	_ = json.Unmarshal(csrfResponse.Body.Bytes(), &csrf)
	cookies := csrfResponse.Result().Cookies()
	form := url.Values{"csrf_token": {csrf.Token}, "return_to": {"/"}}
	begin := browserRequest(s, "POST", "/api/auth/local/login", form, cookies)
	if begin.Code != 303 {
		t.Fatal(begin.Code, begin.Body.String())
	}
	callback := begin.Header().Get("Location")
	wrong := browserRequest(s, "GET", callback, nil, nil)
	if !strings.Contains(wrong.Header().Get("Location"), "AUTH_STATE_INVALID") {
		t.Fatal("unbound callback accepted")
	}
	complete := browserRequest(s, "GET", callback, nil, cookies)
	if complete.Header().Get("Location") != "/" {
		t.Fatal("login failed", complete.Header().Get("Location"))
	}
	sessionCookies := complete.Result().Cookies()
	cookies = append(cookies, sessionCookies...)
	me := browserRequest(s, "GET", "/api/auth/me", nil, cookies)
	if me.Code != 200 {
		t.Fatal(me.Code, me.Body.String())
	}
	var first struct{ User User }
	_ = json.Unmarshal(me.Body.Bytes(), &first)
	replay := browserRequest(s, "GET", callback, nil, cookies)
	if !strings.Contains(replay.Header().Get("Location"), "AUTH_STATE_INVALID") {
		t.Fatal("callback replay accepted")
	}
	badForm := url.Values{"csrf_token": {"bad"}}
	if browserRequest(s, "POST", "/api/auth/local/login", badForm, cookies).Code != 403 {
		t.Fatal("invalid csrf accepted")
	}
	csrfResponse = browserRequest(s, "GET", "/api/auth/csrf", nil, cookies)
	_ = json.Unmarshal(csrfResponse.Body.Bytes(), &csrf)
	form.Set("csrf_token", csrf.Token)
	logout := browserRequest(s, "POST", "/api/auth/logout", form, cookies)
	if logout.Code != 204 {
		t.Fatal(logout.Code, logout.Body.String())
	}
	if browserRequest(s, "GET", "/api/auth/me", nil, cookies).Code != 401 {
		t.Fatal("logout cookie replay accepted")
	}
	// The next authenticated login maps to the same stable source/subject account.
	csrfResponse = browserRequest(s, "GET", "/api/auth/csrf", nil, csrfResponse.Result().Cookies())
	_ = json.Unmarshal(csrfResponse.Body.Bytes(), &csrf)
	cookies = csrfResponse.Result().Cookies()
	form.Set("csrf_token", csrf.Token)
	begin = browserRequest(s, "POST", "/api/auth/local/login", form, cookies)
	complete = browserRequest(s, "GET", begin.Header().Get("Location"), nil, cookies)
	cookies = append(cookies, complete.Result().Cookies()...)
	me = browserRequest(s, "GET", "/api/auth/me", nil, cookies)
	var second struct{ User User }
	_ = json.Unmarshal(me.Body.Bytes(), &second)
	if second.User.ID != first.User.ID {
		t.Fatal("stable identity created a second user")
	}
	_ = store
}
func TestSessionCSRFAndAmbiguousCredential(t *testing.T) {
	s, store := testServer(t)
	token := randomToken()
	_ = store.Update(context.Background(), PartitionAll, func(st *State) error {
		st.Sessions[digest(token)] = Session{User: "admin", AuthVersion: 1, Seen: now(), Absolute: now().Add(time.Hour), CSRF: "csrf"}
		return nil
	})
	r := httptest.NewRequest("POST", "/api/v1/orgs", strings.NewReader(`{"name":"X"}`))
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(&http.Cookie{Name: s.cookieName("session"), Value: token})
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("missing csrf accepted")
	}
	r.Header.Set("Origin", s.Config.PublicURL)
	r.Header.Set("X-CSRF-Token", "csrf")
	r.Header.Set("Authorization", "Bearer krun_cli_admin")
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatal("ambiguous credentials accepted")
	}
}
func TestDeviceApprovalAndSingleConsumption(t *testing.T) {
	s, store := testServer(t)
	w := request(s, "POST", "/api/auth/cli/start", "", `{}`)
	var device struct {
		DeviceCode string `json:"device_code"`
		UserCode   string `json:"user_code"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &device)
	raw := fmt.Sprintf(`{"device_code":%q}`, device.DeviceCode)
	if request(s, "POST", "/api/auth/cli/poll", "", raw).Code != 409 {
		t.Fatal("unapproved device issued token")
	}
	if request(s, "POST", "/api/auth/cli/approve", "krun_cli_admin", fmt.Sprintf(`{"user_code":%q}`, device.UserCode)).Code != 403 {
		t.Fatal("CLI may not approve another CLI")
	}
	_ = store.Update(context.Background(), PartitionAll, func(st *State) error {
		d := st.Devices[digest(device.DeviceCode)]
		d.Approved = true
		d.Token = "krun_cli_test"
		st.Devices[d.CodeHash] = d
		return nil
	})
	if request(s, "POST", "/api/auth/cli/poll", "", raw).Code != 200 {
		t.Fatal("approved device cannot poll")
	}
	if request(s, "POST", "/api/auth/cli/poll", "", raw).Code != 400 {
		t.Fatal("device token issued twice")
	}
}
func TestMappingPreservesLargeInteger(t *testing.T) {
	d := json.NewDecoder(strings.NewReader(`{"data":{"id":9007199254740993}}`))
	d.UseNumber()
	var value any
	_ = d.Decode(&value)
	id, e := mapped(value, "data.id", true)
	if e != nil || id != "9007199254740993" {
		t.Fatal(id, e)
	}
}

func TestLoginGenerationAndSessionExpiration(t *testing.T) {
	s, store := testServer(t)
	w := browserRequest(s, "GET", "/api/auth/csrf", nil, nil)
	var csrf struct {
		Token string `json:"csrf_token"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &csrf)
	cookies := w.Result().Cookies()
	form := url.Values{"csrf_token": {csrf.Token}}
	first := browserRequest(s, "POST", "/api/auth/local/login", form, cookies)
	second := browserRequest(s, "POST", "/api/auth/local/login", form, cookies)
	late := browserRequest(s, "GET", first.Header().Get("Location"), nil, cookies)
	if !strings.Contains(late.Header().Get("Location"), "AUTH_STATE_INVALID") {
		t.Fatal("superseded login accepted")
	}
	good := browserRequest(s, "GET", second.Header().Get("Location"), nil, cookies)
	if good.Header().Get("Location") != "/" {
		t.Fatal("current generation login failed")
	}
	_ = store.Update(context.Background(), PartitionAll, func(st *State) error {
		session := st.Sessions[digest("krun_cli_viewer")]
		session.Absolute = now().Add(-time.Minute)
		st.Sessions[session.Hash] = session
		st.Sessions[digest("krun_cli_viewer")] = session
		return nil
	})
	if request(s, "GET", "/api/v1/me", "krun_cli_viewer", "").Code != 401 {
		t.Fatal("absolute timeout ignored")
	}
}
func TestRemovedSourceInvalidatesExistingCredentials(t *testing.T) {
	s, _ := testServer(t)
	delete(s.providers, "local")
	if request(s, "GET", "/api/v1/me", "krun_cli_admin", "").Code != 401 {
		t.Fatal("disabled source accepted")
	}
}
func TestConfigurationGuardsAndSourceTombstones(t *testing.T) {
	c := Config{DB: "postgres://test-only/krun_test", Development: true, PublicURL: "http://0.0.0.0:8080"}
	if c.Validate() == nil {
		t.Fatal("development public_url must stay on loopback HTTP")
	}
	c = Config{DB: "postgres://test-only/krun_test", Development: true, PublicURL: "http://127.0.0.1:8080", Listen: "0.0.0.0:9000", InternalListen: "0.0.0.0:9000"}
	if c.Validate() == nil {
		t.Fatal("internal_listen must differ from listen")
	}
	store := memoryStore()
	c.Listen = "127.0.0.1:8080"
	c.InternalListen = ""
	c.Providers = []ProviderConfig{{ID: "local", Source: "source1", Type: "dev", Registration: "jit"}}
	if _, e := NewServer(context.Background(), c, store); e != nil {
		t.Fatal(e)
	}
	c.Providers[0].Source = "source2"
	if _, e := NewServer(context.Background(), c, store); e == nil {
		t.Fatal("source reassignment accepted")
	}
}

func TestInviteOnlyRequiresExactUnexpiredIdentity(t *testing.T) {
	s, store := testServer(t)
	s.providers["local"].config.Registration = "invite_only"
	login := func() string {
		w := browserRequest(s, "GET", "/api/auth/csrf", nil, nil)
		var csrf struct {
			Token string `json:"csrf_token"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &csrf)
		cookies := w.Result().Cookies()
		begin := browserRequest(s, "POST", "/api/auth/local/login", url.Values{"csrf_token": {csrf.Token}}, cookies)
		return browserRequest(s, "GET", begin.Header().Get("Location"), nil, cookies).Header().Get("Location")
	}
	if !strings.Contains(login(), "REGISTRATION_DENIED") {
		t.Fatal("uninvited user registered")
	}
	_ = store.Update(context.Background(), PartitionAll, func(st *State) error { st.Invitations["local\x00developer"] = now().Add(-time.Minute); return nil })
	if !strings.Contains(login(), "REGISTRATION_DENIED") {
		t.Fatal("expired invitation accepted")
	}
	if e := Invite(context.Background(), store, "local", "developer"); e != nil {
		t.Fatal(e)
	}
	if login() != "/" {
		t.Fatal("exact invitation rejected")
	}
	_ = store.Update(context.Background(), PartitionAll, func(st *State) error {
		if _, ok := st.Invitations["local\x00developer"]; ok {
			t.Fatal("invitation not consumed")
		}
		return nil
	})
}
