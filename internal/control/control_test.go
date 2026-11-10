package control

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	identityprovider "github.com/krun-dev/krun/internal/adapters/identity"
	appconfig "github.com/krun-dev/krun/internal/config"
)

func fixture() (*State, User, User) {
	st := newState()
	admin := User{ID: "admin", Source: "local", Subject: "a", Status: "active", AuthVersion: 1}
	viewer := User{ID: "viewer", Source: "local", Subject: "b", Status: "active", AuthVersion: 1}
	st.Users[admin.ID] = admin
	st.Users[viewer.ID] = viewer
	for _, id := range []string{"org-a", "org-b"} {
		st.Organizations[id] = Organization{ID: id, Name: id}
		st.Members[memberKey(id, admin.ID)] = Member{Organization: id, User: admin.ID}
	}
	st.Members[memberKey("org-a", viewer.ID)] = Member{Organization: "org-a", User: viewer.ID}
	st.Assignments["admin"] = Assignment{ID: "admin", SubjectKind: "user", SubjectID: admin.ID, Role: "system_admin", ScopeKind: "system", ScopeID: "system"}
	st.Assignments["viewer"] = Assignment{ID: "viewer", SubjectKind: "user", SubjectID: viewer.ID, Role: "viewer", ScopeKind: "organization", ScopeID: "org-a"}
	st.Resources["svc-a"] = Resource{ID: "svc-a", Kind: "worker", Organization: "org-a", Name: "A", Revision: 1}
	st.Resources["svc-b"] = Resource{ID: "svc-b", Kind: "worker", Organization: "org-b", Name: "B", Revision: 1}
	return st, admin, viewer
}

func testServer(t *testing.T) (*Server, *Store) {
	t.Helper()
	store := postgresTestStore(t)
	c := appconfig.Config{DB: "postgres://test-only/krun_test", Development: true, PublicURL: "http://127.0.0.1:8080", Providers: []appconfig.ProviderConfig{{ID: "local", Source: "local", Type: "dev", Registration: "jit"}}}
	s, e := NewServer(context.Background(), c, store)
	if e != nil {
		t.Fatal(e)
	}
	st, admin, viewer := fixture()
	st.Bootstrapped = true
	ctx := context.Background()
	if e = store.Update(ctx, PartitionIAM, func(state *State) error {
		st.Sources = state.Sources
		st.Revision = state.Revision + 1
		*state = *st
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	for _, user := range []User{admin, viewer} {
		token := "krun_cli_" + user.ID
		session := Session{User: user.ID, AuthVersion: user.AuthVersion, CLI: true, Created: now(), Seen: now(), Absolute: now().Add(time.Hour)}
		if e = store.InsertSession(ctx, token, session); e != nil {
			t.Fatal(e)
		}
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
	}{{"GET", "/api/v1/orgs", "", "", 401}, {"GET", "/api/v1/orgs/org-b/members", "krun_cli_viewer", "", 403}, {"POST", "/api/v1/orgs/org-a/role-assignments", "krun_cli_admin", `{"subject_kind":"user","subject_id":"viewer","role_id":"system_admin","scope_kind":"system","scope_id":"system","conditions":{}}`, 422}, {"POST", "/api/v1/orgs", "krun_cli_admin", `{"name":"A","name":"B"}`, 400}, {"POST", "/api/v1/orgs", "krun_cli_admin", `{"name":"A","owner_user_id":"viewer"}`, 400}, {"PATCH", "/api/v1/system/users/admin", "krun_cli_admin", `{"status":"disabled"}`, 409}, {"DELETE", "/api/v1/system/role-assignments/admin", "krun_cli_admin", "", 409}, {"POST", "/api/v1/authz/check", "krun_cli_admin", `{"action":"worker.read","resource_kind":"worker","resource_id":"svc-a","principal_id":"admin"}`, 400}, {"GET", "/api/v1/orgs/org-b/workers/svc-a", "krun_cli_admin", "", 404}}
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
	raw := `{"name":"test","expires_in":3600,"policy":{"schema_version":1,"statements":[{"effect":"allow","actions":["worker.read"],"resource":{"organization_id":"org-a","kind":"worker","ids":["svc-a"]},"conditions":{}}]}}`
	w := request(s, "POST", "/api/v1/orgs/org-a/api-keys", "krun_cli_viewer", raw)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	var issued struct{ ID, Key string }
	if e := json.Unmarshal(w.Body.Bytes(), &issued); e != nil {
		t.Fatal(e)
	}
	if request(s, "GET", "/api/v1/orgs/org-a/workers/svc-a", issued.Key, "").Code != 200 {
		t.Fatal("exact key cannot read")
	}
	if request(s, "GET", "/api/v1/orgs/org-b/workers/svc-b", issued.Key, "").Code != 404 {
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
	if request(s, "GET", "/api/v1/orgs/org-a/workers/svc-a", issued.Key, "").Code != 404 {
		t.Fatal("removed role remained active")
	}
	_ = store.Update(context.Background(), PartitionAll, func(st *State) error { k := st.Keys[issued.ID]; k.Revoked = true; st.Keys[k.ID] = k; return nil })
	if request(s, "GET", "/api/auth/me", issued.Key, "").Code != 401 {
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
	csrfResponse := browserRequest(s, "POST", "/api/auth/csrf", nil, nil)
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
	csrfResponse = browserRequest(s, "POST", "/api/auth/csrf", nil, cookies)
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
	csrfResponse = browserRequest(s, "POST", "/api/auth/csrf", nil, csrfResponse.Result().Cookies())
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

func TestLoginReturnPathUsesRootQueryNavigation(t *testing.T) {
	for _, tc := range []struct {
		value string
		ok    bool
	}{
		{"", true},
		{"/", true},
		{"/?page=workers&workspace=org_1", true},
		{"/console/", false},
		{"/other", false},
		{"//evil.example", false},
		{"/?page=workers#fragment", false},
		{"https://evil.example/", false},
	} {
		_, err := loginReturnPath(tc.value)
		if (err == nil) != tc.ok {
			t.Errorf("loginReturnPath(%q) error=%v, want ok=%v", tc.value, err, tc.ok)
		}
	}
}

func TestSessionCSRFAndAmbiguousCredential(t *testing.T) {
	s, store := testServer(t)
	token := randomToken()
	session := Session{User: "admin", AuthVersion: 1, Created: now(), Seen: now(), Absolute: now().Add(time.Hour), CSRF: "csrf"}
	if err := store.InsertSession(t.Context(), token, session); err != nil {
		t.Fatal(err)
	}
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

func TestMappingPreservesLargeInteger(t *testing.T) {
	d := json.NewDecoder(strings.NewReader(`{"data":{"id":9007199254740993}}`))
	d.UseNumber()
	var value any
	_ = d.Decode(&value)
	id, e := identityprovider.Mapped(value, "data.id", true)
	if e != nil || id != "9007199254740993" {
		t.Fatal(id, e)
	}
}

func TestLoginGenerationAndSessionExpiration(t *testing.T) {
	s, store := testServer(t)
	w := browserRequest(s, "POST", "/api/auth/csrf", nil, nil)
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
	expireTestSession(t, store, "krun_cli_viewer")
	if request(s, "GET", "/api/auth/me", "krun_cli_viewer", "").Code != 401 {
		t.Fatal("absolute timeout ignored")
	}
}

func TestRemovedSourceInvalidatesExistingCredentials(t *testing.T) {
	s, _ := testServer(t)
	delete(s.providers, "local")
	if request(s, "GET", "/api/auth/me", "krun_cli_admin", "").Code != 401 {
		t.Fatal("disabled source accepted")
	}
}

func TestConfigurationGuardsAndSourceTombstones(t *testing.T) {
	c := appconfig.Config{DB: "postgres://test-only/krun_test", Development: true, PublicURL: "http://0.0.0.0:8080"}
	if c.Validate() == nil {
		t.Fatal("development public_url must stay on loopback HTTP")
	}
	c = appconfig.Config{DB: "postgres://test-only/krun_test", Development: true, PublicURL: "http://127.0.0.1:8080", Listen: "127.0.0.1:9000", InternalListen: "127.0.0.1:9000"}
	if c.Validate() == nil {
		t.Fatal("internal_listen must differ from listen")
	}
	c = appconfig.Config{DB: "postgres://test-only/krun_test", Development: true, PublicURL: "http://127.0.0.1:8080", Workers: catalogConfig()}
	if c.Validate() == nil {
		t.Fatal("Workers without internal_listen accepted")
	}
	c.InternalListen = "127.0.0.1:9001"
	if err := c.Validate(); err != nil {
		t.Fatal("Workers with internal_listen rejected:", err)
	}
	store := postgresTestStore(t)
	c.Listen = "127.0.0.1:8080"
	c.InternalListen = ""
	c.Workers = nil
	c.Providers = []appconfig.ProviderConfig{{ID: "local", Source: "source1", Type: "dev", Registration: "jit"}}
	if _, e := NewServer(context.Background(), c, store); e != nil {
		t.Fatal(e)
	}
	c.Providers[0].Source = "source2"
	if _, e := NewServer(context.Background(), c, store); e == nil {
		t.Fatal("source reassignment accepted")
	}
}

func TestProtectedRejectsAnonymousBeforeLoadingState(t *testing.T) {
	s := &Server{
		Config: appconfig.Config{Development: true, PublicURL: "http://127.0.0.1:8080"},
		Store:  &Store{},
	}
	s.routes()
	w := request(s, http.MethodGet, "/api/auth/me", "", "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous request reached the uninitialized store: %d %s", w.Code, w.Body.String())
	}
	w = request(s, http.MethodPost, "/api/v1/orgs", "", strings.Repeat("x", (1<<20)+1))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous request body was read before rejection: %d %s", w.Code, w.Body.String())
	}
}

func TestCSRFIssuanceIsPostOnlyAndRateLimited(t *testing.T) {
	s, store := testServer(t)
	if w := browserRequest(s, http.MethodGet, "/api/auth/csrf", nil, nil); w.Code != http.StatusNotFound {
		t.Fatalf("GET CSRF status=%d, want 404", w.Code)
	}
	for i := 0; i < 30; i++ {
		if w := browserRequest(s, http.MethodPost, "/api/auth/csrf", nil, nil); w.Code != http.StatusOK {
			t.Fatalf("CSRF request %d failed: %d %s", i+1, w.Code, w.Body.String())
		}
	}
	for i := 0; i < 10; i++ {
		if w := browserRequest(s, http.MethodPost, "/api/auth/csrf", nil, nil); w.Code != http.StatusTooManyRequests {
			t.Fatalf("over-limit CSRF request accepted: %d %s", w.Code, w.Body.String())
		}
	}
	var count int
	if err := postgresTestPool(store).QueryRow(t.Context(), "SELECT count(*) FROM krun_browsers").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 30 {
		t.Fatalf("browser rows=%d, want bounded 30", count)
	}

}

func TestDeniedReadsDoNotAppendAuditRows(t *testing.T) {
	s, store := testServer(t)
	if w := request(s, http.MethodGet, "/api/v1/orgs/org-b", "krun_cli_viewer", ""); w.Code < 400 {
		t.Fatalf("denied read status=%d body=%s", w.Code, w.Body.String())
	}
	if len(testAudits(t, store)) != 0 {
		t.Fatalf("denied read appended %d audit rows", len(testAudits(t, store)))
	}
}

func TestCallbackFailuresAndRateLimitsDoNotAppendAuditRows(t *testing.T) {
	s, store := testServer(t)
	path := "/api/auth/local/callback?state=invalid&code=invalid"
	var last *httptest.ResponseRecorder
	for i := 0; i < 121; i++ {
		last = browserRequest(s, http.MethodGet, path, nil, nil)
	}
	if location := last.Header().Get("Location"); !strings.Contains(location, "RATE_LIMITED") {
		t.Fatalf("callback was not rate limited: status=%d location=%q", last.Code, location)
	}
	if len(testAudits(t, store)) != 0 {
		t.Fatalf("attacker-controlled callback failures appended %d audit rows", len(testAudits(t, store)))
	}
}

func TestClientIPTrustsOnlyConfiguredProxyChain(t *testing.T) {
	_, trusted, _ := net.ParseCIDR("10.0.0.0/8")
	s := &Server{trustedProxies: []*net.IPNet{trusted}}
	cases := []struct {
		name, remote, forwarded, want string
	}{
		{"direct-spoof-ignored", "203.0.113.8:4444", "198.51.100.2", "203.0.113.8"},
		{"trusted-proxy", "10.0.0.4:443", "198.51.100.2", "198.51.100.2"},
		{"trusted-chain", "10.0.0.4:443", "198.51.100.2, 10.1.2.3", "198.51.100.2"},
		{"malformed-chain", "10.0.0.4:443", "198.51.100.2, garbage", "10.0.0.4"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = tc.remote
			r.Header.Set("X-Forwarded-For", tc.forwarded)
			if got := s.clientIP(r); got != tc.want {
				t.Fatalf("clientIP=%q want %q", got, tc.want)
			}
		})
	}
}

func TestRequiredListenerFailureStopsServerGroup(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err = serveHTTPServers(ctx, []*http.Server{{Addr: occupied.Addr().String(), Handler: http.NotFoundHandler()}})
	if err == nil || !strings.Contains(err.Error(), occupied.Addr().String()) {
		t.Fatalf("listener bind failure was not propagated: %v", err)
	}
}

func TestInviteOnlyRequiresExactUnexpiredIdentity(t *testing.T) {
	s, store := testServer(t)
	s.providers["local"].Config.Registration = "invite_only"
	login := func() string {
		w := browserRequest(s, "POST", "/api/auth/csrf", nil, nil)
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

func TestJSONLoginStartKeepsRedirectContract(t *testing.T) {
	server, _ := testServer(t)
	csrf := browserRequest(server, http.MethodPost, "/api/auth/csrf", nil, nil)
	var challenge struct {
		Token string `json:"csrf_token"`
	}
	if err := json.Unmarshal(csrf.Body.Bytes(), &challenge); err != nil {
		t.Fatal(err)
	}
	form := url.Values{"return_to": {"/"}, "csrf_token": {challenge.Token}}
	req := httptest.NewRequest(http.MethodPost, "/api/auth/local/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Origin", server.Config.PublicURL)
	cookies := csrf.Result().Cookies()
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	server.ServeHTTP(response, req)
	var start struct {
		Redirect string `json:"redirect_url"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &start) != nil || start.Redirect == "" {
		t.Fatalf("JSON login: %d %s", response.Code, response.Body.String())
	}
	callback := browserRequest(server, http.MethodGet, start.Redirect, nil, cookies)
	if callback.Code != http.StatusSeeOther || callback.Header().Get("Location") != "/" {
		t.Fatalf("callback: %d %s", callback.Code, callback.Header().Get("Location"))
	}
	cookies = append(cookies, callback.Result().Cookies()...)
	me := browserRequest(server, http.MethodGet, "/api/auth/me", nil, cookies)
	if me.Code != http.StatusOK {
		t.Fatalf("login session missing: %d %s", me.Code, me.Body.String())
	}
}
